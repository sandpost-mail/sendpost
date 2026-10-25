// Package delivery is the outbound side of Sandpost: the per project relay
// (transport) and policy settings, manual release, and the job that sends
// captured mail through the relay after the policy engine (internal/policy)
// allowed it.
//
// Everything here is project scoped: methods take an authz.Scope and every
// query restricts project_id. The only way mail leaves is Worker.process and
// it sends nothing but what policy.Evaluate or policy.EvaluateRelease return,
// re-evaluated from the stored state on every attempt.
package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/secrets"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Domain errors.
var (
	// ErrInvalid wraps a rejected input, its message is safe to show.
	ErrInvalid = errors.New("invalid input")
	// ErrNotFound means there is nothing of that kind in the project.
	ErrNotFound = errors.New("not found")
)

// Run names. Rows of one delivery run share a run key.
const (
	// RunAuto is the run the policy starts after capture.
	RunAuto       = "auto"
	runReleaseTag = "release:"
)

// Delivery statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusBlocked = "blocked"
)

// Delivery is the outcome for one recipient of a message.
type Delivery struct {
	ID                 int64      `json:"id"`
	MessageID          int64      `json:"message_id"`
	Run                string     `json:"run"`
	Kind               string     `json:"kind"`
	Recipient          string     `json:"recipient"`
	EffectiveRecipient string     `json:"effective_recipient"`
	Status             string     `json:"status"`
	Error              string     `json:"error"`
	Attempts           int        `json:"attempts"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	SentAt             *time.Time `json:"sent_at"`
}

// Queue schedules delivery jobs inside the caller's transaction, so a job
// exists exactly when the rows that justify it were committed.
type Queue interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args DeliverArgs) error
}

// Service manages transports, policies and releases of a project.
type Service struct {
	db    store.DB
	q     *sqlcgen.Queries
	box   *secrets.Box
	relay Relay
	queue Queue
}

// NewService returns a Service. secretKey is the instance SECRET_KEY, relay
// is used by the connection test, queue schedules releases (nil disables
// release).
func NewService(db store.DB, secretKey string, relay Relay, queue Queue) *Service {
	return &Service{db: db, q: sqlcgen.New(db), box: secrets.New(secretKey), relay: relay, queue: queue}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func secretContext(projectID int64) string { return fmt.Sprintf("transport:%d", projectID) }

func newRunKey() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random run key: %w", err)
	}
	return runReleaseTag + hex.EncodeToString(b[:]), nil
}

func toDelivery(r sqlcgen.Delivery) Delivery {
	return Delivery{
		ID: r.ID, MessageID: r.MessageID, Run: r.Run, Kind: r.Kind, Recipient: r.Recipient,
		EffectiveRecipient: r.EffectiveRecipient, Status: r.Status, Error: r.Error, Attempts: int(r.Attempts),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, SentAt: r.SentAt,
	}
}
