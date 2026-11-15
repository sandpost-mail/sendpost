package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// ListDeliveries returns the delivery rows of a message of the project, oldest
// first. It returns messages.ErrNotFound for a message that is not in the
// project, exactly like an unknown id.
func (s *Service) ListDeliveries(ctx context.Context, scope authz.Scope, messageID int64) ([]Delivery, error) {
	ok, err := s.q.MessageExists(ctx, sqlcgen.MessageExistsParams{ProjectID: scope.ProjectID(), ID: messageID})
	if err != nil {
		return nil, fmt.Errorf("check message: %w", err)
	}
	if !ok {
		return nil, messages.ErrNotFound
	}
	rows, err := s.q.ListMessageDeliveries(ctx, sqlcgen.ListMessageDeliveriesParams{ProjectID: scope.ProjectID(), MessageID: messageID})
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDelivery(r))
	}
	return out, nil
}

// Release sends a captured message to explicit addresses. Every target must
// pass the project's allowlist and the project needs an enabled transport,
// otherwise nothing is queued and the error wraps policy.ErrBlocked. The
// release is a job like any delivery, which evaluates the engine again when it
// runs. It returns the pending delivery rows.
func (s *Service) Release(ctx context.Context, scope authz.Scope, messageID int64, targets []string) ([]Delivery, error) {
	if s.queue == nil {
		return nil, errors.New("release: no job queue configured")
	}
	var out []Delivery
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = s.release(ctx, tx, scope.ProjectID(), messageID, targets)
		return err
	})
	switch {
	case errors.Is(err, errNoMessage):
		return nil, messages.ErrNotFound
	case errors.Is(err, policy.ErrBlocked):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("release message: %w", err)
	}
	return out, nil
}

var errNoMessage = errors.New("message not in project")

// release validates the targets and queues the job. It never reads the
// message body: the job loads and rewrites it when it runs, so the request
// does not hold a copy of a possibly large message.
func (s *Service) release(ctx context.Context, tx pgx.Tx, projectID, messageID int64, targets []string) ([]Delivery, error) {
	q := sqlcgen.New(tx)
	ok, err := q.MessageExists(ctx, sqlcgen.MessageExistsParams{ProjectID: projectID, ID: messageID})
	if err != nil {
		return nil, fmt.Errorf("check message: %w", err)
	}
	if !ok {
		return nil, errNoMessage
	}
	in, err := loadSettings(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	plan, err := policy.CheckRelease(policy.ReleaseInput{
		Policy: in.policy, Transport: in.transport, Targets: targets,
	})
	if err != nil {
		return nil, err
	}
	run, err := newRunKey()
	if err != nil {
		return nil, err
	}
	for _, d := range plan.Decisions {
		err := q.UpsertDelivery(ctx, sqlcgen.UpsertDeliveryParams{
			ProjectID: projectID, MessageID: messageID, Run: run, Kind: kindRelease, Recipient: d.Recipient,
			EffectiveRecipient: d.Effective, Status: StatusPending,
		})
		if err != nil {
			return nil, fmt.Errorf("insert delivery: %w", err)
		}
	}
	args := DeliverArgs{ProjectID: projectID, MessageID: messageID, Run: run, Targets: targetsOf(plan)}
	if err := s.queue.InsertTx(ctx, tx, args); err != nil {
		return nil, fmt.Errorf("enqueue release: %w", err)
	}
	if err := audit.Record(ctx, q, audit.Entry{
		Action: "message.release", ProjectID: projectID, Target: fmt.Sprintf("message:%d", messageID),
		Detail: map[string]any{"targets": args.Targets},
	}); err != nil {
		return nil, err
	}
	rows, err := q.ListRunDeliveries(ctx, sqlcgen.ListRunDeliveriesParams{ProjectID: projectID, MessageID: messageID, Run: run})
	if err != nil {
		return nil, fmt.Errorf("list run: %w", err)
	}
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDelivery(r))
	}
	return out, nil
}

func targetsOf(plan policy.Plan) []string {
	out := make([]string, 0, len(plan.Decisions))
	for _, d := range plan.Decisions {
		out = append(out, d.Recipient)
	}
	return out
}
