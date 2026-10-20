// Package audit records administrative actions. Services call Record with the
// transaction that performs the change, so an action and its audit row commit
// or roll back together. The actor travels in the context.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

type (
	actorKey  struct{}
	sourceKey struct{}
)

// WithSource returns a context whose audit entries record where the action
// came from (for example "cli") as "actor" in their detail. It is meant for
// actions without a user actor.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// Source returns the source attributed to ctx, false when there is none.
func Source(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(sourceKey{}).(string)
	return s, ok && s != ""
}

// WithActor returns a context whose audit entries are attributed to userID.
func WithActor(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

// Actor returns the user attributed to ctx, false for system actions.
func Actor(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(actorKey{}).(int64)
	return id, ok && id != 0
}

// Entry is one audit record. Detail must never contain secrets (passwords,
// tokens, credential secrets): it is readable through the API.
type Entry struct {
	Action string
	// ProjectID is zero for instance level actions.
	ProjectID int64
	Target    string
	Detail    map[string]any
}

// Record writes e using q, normally a transaction bound Queries.
func Record(ctx context.Context, q *sqlcgen.Queries, e Entry) error {
	detail := make(map[string]any, len(e.Detail)+1)
	for k, v := range e.Detail {
		detail[k] = v
	}
	if src, ok := Source(ctx); ok {
		detail["actor"] = src
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	p := sqlcgen.InsertAuditParams{Action: e.Action, Target: e.Target, Detail: raw}
	if id, ok := Actor(ctx); ok {
		p.ActorUserID = pgtype.Int8{Int64: id, Valid: true}
	}
	if e.ProjectID != 0 {
		p.ProjectID = pgtype.Int8{Int64: e.ProjectID, Valid: true}
	}
	if err := q.InsertAudit(ctx, p); err != nil {
		return fmt.Errorf("record audit %s: %w", e.Action, err)
	}
	return nil
}
