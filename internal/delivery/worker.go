package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/secrets"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

const (
	// MaxAttempts bounds the tries of one delivery job. River spaces them with
	// its default backoff (about 1s, 16s, 81s, 4m), so a relay that stays down
	// is given up on after roughly five minutes.
	MaxAttempts = 5
	// jobTimeout bounds one attempt, including every SMTP step.
	jobTimeout  = 3 * time.Minute
	workerCount = 4
	// DeliverKind is the River job kind.
	DeliverKind = "deliver_message"

	kindRelease = "release"
)

// DeliverArgs is the payload of a delivery job. It carries ids and, for a
// release, the targets, never the message and never a secret: everything else
// is read again from the database when the job runs.
type DeliverArgs struct {
	ProjectID int64  `json:"project_id"`
	MessageID int64  `json:"message_id"`
	Run       string `json:"run"`
	// Targets is set for a release.
	Targets []string `json:"targets,omitempty"`
}

// Kind implements river.JobArgs.
func (DeliverArgs) Kind() string { return DeliverKind }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DeliverArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: MaxAttempts} }

func (a DeliverArgs) isRelease() bool { return strings.HasPrefix(a.Run, runReleaseTag) }

// RiverQueue is the Queue backed by River.
type RiverQueue struct {
	client *river.Client[pgx.Tx]
}

// NewRiverQueue returns a Queue inserting through client.
func NewRiverQueue(client *river.Client[pgx.Tx]) *RiverQueue { return &RiverQueue{client: client} }

// InsertTx implements Queue.
func (q *RiverQueue) InsertTx(ctx context.Context, tx pgx.Tx, args DeliverArgs) error {
	_, err := q.client.InsertTx(ctx, tx, args, nil)
	return err
}

// NewRiverClient builds the River client. With a worker it also runs jobs
// (call Start), without one it can only insert, which is what a process that
// merely queues releases needs. opts adjust the River configuration (tests use
// them for a fast retry policy).
func NewRiverClient(pool *pgxpool.Pool, w *Worker, log *slog.Logger, opts ...func(*river.Config)) (*river.Client[pgx.Tx], error) {
	cfg := &river.Config{Logger: log}
	if w != nil {
		workers := river.NewWorkers()
		river.AddWorker(workers, w)
		cfg.Workers = workers
		cfg.Queues = map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: workerCount}}
	}
	for _, opt := range opts {
		opt(cfg)
	}
	c, err := river.NewClient(riverpgxv5.New(pool), cfg)
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	return c, nil
}

// Enqueuer schedules the policy run of a freshly captured message. It is
// called inside the ingest transaction.
type Enqueuer struct {
	queue Queue
}

// NewEnqueuer returns an Enqueuer on queue.
func NewEnqueuer(queue Queue) *Enqueuer { return &Enqueuer{queue: queue} }

// AfterInsert queues a delivery job when the project's policy is not capture.
// A project without a policy is capture, so no job exists and nothing can be
// sent. The worker evaluates the policy again when the job runs.
func (e *Enqueuer) AfterInsert(ctx context.Context, tx pgx.Tx, projectID, messageID int64) error {
	mode, err := sqlcgen.New(tx).DeliveryPolicyMode(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("policy mode: %w", err)
	}
	if mode == string(policy.ModeCapture) {
		return nil
	}
	return e.queue.InsertTx(ctx, tx, DeliverArgs{ProjectID: projectID, MessageID: messageID, Run: RunAuto})
}

// Worker is the River worker that sends mail.
type Worker struct {
	river.WorkerDefaults[DeliverArgs]
	db    store.DB
	q     *sqlcgen.Queries
	box   *secrets.Box
	relay Relay
	log   *slog.Logger
}

// NewWorker returns a Worker.
func NewWorker(db store.DB, secretKey string, relay Relay, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Worker{db: db, q: sqlcgen.New(db), box: secrets.New(secretKey), relay: relay, log: log}
}

// Timeout implements river.Worker.
func (*Worker) Timeout(*river.Job[DeliverArgs]) time.Duration { return jobTimeout }

// Work implements river.Worker.
func (w *Worker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	last := job.Attempt >= job.MaxAttempts
	return w.Process(ctx, job.Args, last)
}

// inputs is the stored state the engine evaluates.
type inputs struct {
	policy    *policy.Policy
	transport policy.TransportState
	row       *sqlcgen.Transport
	message   policy.Message
	raw       []byte
}

// loadInputs reads the message, its recipients, the policy and the transport,
// all restricted to the project. It returns pgx.ErrNoRows when the message is
// not in the project and a *policy.BlockedError when stored policy data can
// not be read.
func loadInputs(ctx context.Context, q *sqlcgen.Queries, projectID, messageID int64) (inputs, error) {
	src, err := q.GetDeliverySource(ctx, sqlcgen.GetDeliverySourceParams{MessageID: messageID, ProjectID: projectID})
	if err != nil {
		return inputs{}, fmt.Errorf("load message: %w", err)
	}
	rcpts, err := q.ListDeliveryRecipients(ctx, sqlcgen.ListDeliveryRecipientsParams{MessageID: messageID, ProjectID: projectID})
	if err != nil {
		return inputs{}, fmt.Errorf("load recipients: %w", err)
	}
	in := inputs{raw: src.Raw, message: policy.Message{Raw: src.Raw}}
	for _, r := range rcpts {
		switch r.Kind {
		case "envelope":
			in.message.EnvelopeTo = append(in.message.EnvelopeTo, r.Address)
		case "to":
			in.message.To = append(in.message.To, r.Address)
		case "cc":
			in.message.Cc = append(in.message.Cc, r.Address)
		}
	}
	prow, err := q.GetDeliveryPolicy(ctx, projectID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return inputs{}, fmt.Errorf("load policy: %w", err)
	default:
		var list []string
		if err := json.Unmarshal(prow.Allowlist, &list); err != nil {
			return inputs{}, &policy.BlockedError{Reason: "the stored allowlist can not be read"}
		}
		in.policy = &policy.Policy{
			Mode: policy.Mode(prow.Mode), Allowlist: list, RedirectAddress: prow.RedirectAddress, SubjectPrefix: prow.SubjectPrefix,
		}
	}
	trow, err := q.GetTransport(ctx, projectID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return inputs{}, fmt.Errorf("load transport: %w", err)
	default:
		in.row = &trow
		in.transport = policy.TransportState{Present: true, Enabled: trow.Enabled, Sender: trow.Sender}
	}
	return in, nil
}

func relayConfigOf(box *secrets.Box, row sqlcgen.Transport) (RelayConfig, error) {
	cfg := RelayConfig{Host: row.Host, Port: int(row.Port), Username: row.Username, TLSMode: row.TlsMode}
	if len(row.SecretEnc) > 0 {
		pw, err := box.Open(row.SecretEnc, secretContext(row.ProjectID))
		if err != nil {
			return RelayConfig{}, err
		}
		cfg.Password = string(pw)
	}
	return cfg, nil
}

// Process runs one attempt of a delivery job. Nothing from an earlier attempt
// authorizes anything: the policy, the transport and the message are read
// again and the engine decides again, so switching a project back to capture
// or disabling its transport stops mail that is still queued. last says that
// no retry will follow. A returned error asks River for a retry.
func (w *Worker) Process(ctx context.Context, a DeliverArgs, last bool) error {
	in, err := loadInputs(ctx, w.q, a.ProjectID, a.MessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the message was deleted, there is nothing left to send
	}
	var be *policy.BlockedError
	if errors.As(err, &be) {
		return w.block(ctx, a, be.Reason)
	}
	if err != nil {
		return err
	}

	var plan policy.Plan
	if a.isRelease() {
		plan, err = policy.EvaluateRelease(policy.ReleaseInput{Policy: in.policy, Transport: in.transport, Message: in.message, Targets: a.Targets})
	} else {
		plan, err = policy.Evaluate(policy.Input{Policy: in.policy, Transport: in.transport, Message: in.message})
	}
	if errors.As(err, &be) {
		return w.block(ctx, a, be.Reason)
	}
	if err != nil {
		return err
	}
	if err := w.recordDecisions(ctx, a, plan); err != nil {
		return err
	}
	if len(plan.Outbound) == 0 {
		return nil
	}
	cfg, err := relayConfigOf(w.box, *in.row)
	if err != nil {
		return w.block(ctx, a, "the stored transport password can not be decrypted")
	}
	return w.send(ctx, a, plan, cfg, last)
}

// block records that the run was refused and ends the job without retry: a
// misconfiguration does not fix itself, and a retry could only ever send more.
func (w *Worker) block(ctx context.Context, a DeliverArgs, reason string) error {
	reason = cleanText(reason, "")
	w.log.Warn("delivery blocked", "project_id", a.ProjectID, "message_id", a.MessageID, "run", runKind(a), "reason", reason)
	if err := w.q.BlockRun(ctx, sqlcgen.BlockRunParams{ProjectID: a.ProjectID, MessageID: a.MessageID, Run: a.Run, Error: reason}); err != nil {
		return fmt.Errorf("block run: %w", err)
	}
	err := w.q.UpsertDelivery(ctx, sqlcgen.UpsertDeliveryParams{
		ProjectID: a.ProjectID, MessageID: a.MessageID, Run: a.Run, Kind: "blocked", Recipient: "",
		Status: StatusBlocked, Error: reason,
	})
	if err != nil {
		return fmt.Errorf("record block: %w", err)
	}
	return nil
}

func runKind(a DeliverArgs) string {
	if a.isRelease() {
		return "release"
	}
	return RunAuto
}

func (w *Worker) recordDecisions(ctx context.Context, a DeliverArgs, plan policy.Plan) error {
	for _, d := range plan.Decisions {
		kind, status := string(d.Action), StatusPending
		switch {
		case d.Action == policy.ActionSkip:
			kind, status = "skip", StatusSkipped
		case a.isRelease():
			kind = kindRelease
		case d.Action == policy.ActionDeliver:
			kind = "deliver"
		}
		err := w.q.UpsertDelivery(ctx, sqlcgen.UpsertDeliveryParams{
			ProjectID: a.ProjectID, MessageID: a.MessageID, Run: a.Run, Kind: kind, Recipient: d.Recipient,
			EffectiveRecipient: d.Effective, Status: status, Error: skipReason(d, status),
		})
		if err != nil {
			return fmt.Errorf("record decision: %w", err)
		}
	}
	return nil
}

func skipReason(d policy.Decision, status string) string {
	if status == StatusSkipped {
		return d.Reason
	}
	return ""
}

func (w *Worker) send(ctx context.Context, a DeliverArgs, plan policy.Plan, cfg RelayConfig, last bool) error {
	rows, err := w.q.ListRunDeliveries(ctx, sqlcgen.ListRunDeliveriesParams{ProjectID: a.ProjectID, MessageID: a.MessageID, Run: a.Run})
	if err != nil {
		return fmt.Errorf("list run: %w", err)
	}
	pending := make(map[string]sqlcgen.Delivery, len(rows))
	for _, r := range rows {
		if r.Status == StatusPending {
			pending[r.Recipient] = r
		}
	}
	remaining := 0
	var lastErr string
	for _, ob := range plan.Outbound {
		sub, ok := ob.Restrict(func(orig string) bool { _, p := pending[orig]; return p })
		if !ok {
			continue
		}
		results := w.relay.Send(ctx, cfg, sub)
		n, text, err := w.record(ctx, a, sub, results, pending)
		if err != nil {
			return err
		}
		remaining += n
		if text != "" {
			lastErr = text
		}
	}
	if remaining == 0 {
		return nil
	}
	if last {
		w.log.Warn("delivery gave up", "project_id", a.ProjectID, "message_id", a.MessageID, "pending", remaining)
		msg := "gave up, retries exhausted: " + lastErr
		if err := w.q.FailPendingRun(ctx, sqlcgen.FailPendingRunParams{
			ProjectID: a.ProjectID, MessageID: a.MessageID, Run: a.Run, Error: cleanText(msg, ""),
		}); err != nil {
			return fmt.Errorf("fail pending: %w", err)
		}
		return nil
	}
	return fmt.Errorf("delivery incomplete: %d recipient(s) pending", remaining)
}

// record stores the relay's answers. It returns how many recipients remain
// pending (transient failures) and the last transient error text.
func (w *Worker) record(ctx context.Context, a DeliverArgs, ob policy.Outbound, results []RcptResult, pending map[string]sqlcgen.Delivery) (int, string, error) {
	byAddr := make(map[string]RcptResult, len(results))
	for _, r := range results {
		byAddr[r.Address] = r
	}
	missing := RcptResult{Err: &RelayError{Stage: stageData, Text: "the relay gave no answer"}}
	remaining, lastErr := 0, ""
	envelope := ob.Recipients()
	for _, orig := range ob.Covers() {
		key := orig
		if ob.Kind() == policy.KindRedirect {
			key = envelope[0] // all redirected recipients share one message
		}
		res, ok := byAddr[key]
		if !ok {
			res = missing
		}
		row := pending[orig]
		var err error
		switch {
		case res.Err == nil:
			err = w.q.MarkDeliverySent(ctx, sqlcgen.MarkDeliverySentParams{ProjectID: a.ProjectID, ID: row.ID})
		case res.Err.Permanent:
			err = w.q.MarkDeliveryFailed(ctx, sqlcgen.MarkDeliveryFailedParams{ProjectID: a.ProjectID, ID: row.ID, Error: res.Err.Text})
		default:
			remaining++
			lastErr = res.Err.Text
			err = w.q.RecordDeliveryRetry(ctx, sqlcgen.RecordDeliveryRetryParams{ProjectID: a.ProjectID, ID: row.ID, Error: res.Err.Text})
		}
		if err != nil {
			return 0, "", fmt.Errorf("record result: %w", err)
		}
	}
	return remaining, lastErr, nil
}
