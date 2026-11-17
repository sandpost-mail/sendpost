package delivery_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/secrets"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// stubRelay answers Send with whatever answer returns.
type stubRelay struct {
	answer func(ctx context.Context, ob policy.Outbound) []delivery.RcptResult
}

func (s stubRelay) Send(ctx context.Context, _ delivery.RelayConfig, ob policy.Outbound) []delivery.RcptResult {
	return s.answer(ctx, ob)
}

func (stubRelay) Check(context.Context, delivery.RelayConfig) delivery.CheckResult {
	return delivery.CheckResult{}
}

func acceptAll(ob policy.Outbound) []delivery.RcptResult {
	out := make([]delivery.RcptResult, 0)
	for _, r := range ob.Recipients() {
		out = append(out, delivery.RcptResult{Address: r})
	}
	return out
}

// faultyDB fails every statement whose sqlc name is in fail.
type faultyDB struct {
	store.DB
	fail string
}

var errInjected = errors.New("injected database failure")

func (f faultyDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "-- name: "+f.fail+" ") {
		return pgconn.CommandTag{}, errInjected
	}
	return f.DB.Exec(ctx, sql, args...)
}

func faultEnv(t *testing.T) (*env, int64) {
	t.Helper()
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	return e, e.capture("a@corp.example")
}

func TestMissingRelayAnswerIsTransient(t *testing.T) {
	e, mid := faultEnv(t)
	w := delivery.NewWorker(e.pool, secrets.NewBox(secrets.MustDeriveKeys(testKey).Transport), stubRelay{answer: func(context.Context, policy.Outbound) []delivery.RcptResult {
		return nil // the relay said nothing about the recipient
	}}, nil)
	if err := w.Process(e.ctx, e.auto(mid), false); err == nil {
		t.Fatal("a missing answer must ask for a retry")
	}
	rows := e.deliveries(mid)
	if len(rows) != 1 || rows[0].Status != delivery.StatusPending || !strings.Contains(rows[0].Error, "no answer") {
		t.Fatalf("rows %+v", rows)
	}
}

// TestOutcomeIsRecordedAfterTheJobContextEnds pins U3-2: the relay accepted
// the message right as the job context ended (deadline, shutdown). The row
// must still be marked sent, otherwise the next attempt sends it again.
func TestOutcomeIsRecordedAfterTheJobContextEnds(t *testing.T) {
	e, mid := faultEnv(t)
	jobCtx, cancel := context.WithCancel(e.ctx)
	w := delivery.NewWorker(e.pool, secrets.NewBox(secrets.MustDeriveKeys(testKey).Transport), stubRelay{answer: func(_ context.Context, ob policy.Outbound) []delivery.RcptResult {
		cancel()
		return acceptAll(ob)
	}}, nil)
	if err := w.Process(jobCtx, e.auto(mid), false); err != nil {
		t.Fatalf("process: %v", err)
	}
	if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusSent {
		t.Fatalf("statuses %v", st)
	}
}

func TestRecordErrorAfterSend(t *testing.T) {
	e, mid := faultEnv(t)
	sends := 0
	relay := stubRelay{answer: func(_ context.Context, ob policy.Outbound) []delivery.RcptResult {
		sends++
		return acceptAll(ob)
	}}
	w := delivery.NewWorker(faultyDB{DB: e.pool, fail: "MarkDeliverySent"}, secrets.NewBox(secrets.MustDeriveKeys(testKey).Transport), relay, nil)

	// Not the last attempt: the error asks River for a retry, the row stays
	// pending (at least once delivery, documented).
	if err := w.Process(e.ctx, e.auto(mid), false); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the record error", err)
	}
	if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusPending {
		t.Fatalf("statuses %v", st)
	}

	// The last attempt: River gives up, so Work moves the row to failed
	// instead of leaving it pending forever.
	job := &river.Job[delivery.DeliverArgs]{
		JobRow: &rivertype.JobRow{Attempt: delivery.MaxAttempts, MaxAttempts: delivery.MaxAttempts},
		Args:   e.auto(mid),
	}
	if err := w.Work(e.ctx, job); !errors.Is(err, errInjected) {
		t.Fatalf("work: %v", err)
	}
	rows := e.deliveries(mid)
	if len(rows) != 1 || rows[0].Status != delivery.StatusFailed || !strings.Contains(rows[0].Error, "gave up") {
		t.Fatalf("rows %+v", rows)
	}
	if sends != 2 {
		t.Errorf("sends %d", sends)
	}
}

// TestNoUsableAuthIsNotRetried runs the worker against a relay that offers no
// AUTH mechanism Sandpost speaks: the delivery fails at once, no retry.
func TestNoUsableAuthIsNotRetried(t *testing.T) {
	e, mid := faultEnv(t)
	w := delivery.NewWorker(e.pool, secrets.NewBox(secrets.MustDeriveKeys(testKey).Transport), stubRelay{answer: func(_ context.Context, ob policy.Outbound) []delivery.RcptResult {
		out := acceptAll(ob)
		for i := range out {
			out[i].Err = &delivery.RelayError{Stage: "auth", Text: "the relay offers no usable AUTH mechanism", Permanent: true}
		}
		return out
	}}, nil)
	if err := w.Process(e.ctx, e.auto(mid), false); err != nil {
		t.Fatalf("a permanent failure must not ask for a retry: %v", err)
	}
	if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusFailed {
		t.Fatalf("statuses %v", st)
	}
}
