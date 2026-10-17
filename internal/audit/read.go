package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Page size bounds.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ErrInvalidCursor is returned for a malformed cursor.
var ErrInvalidCursor = errors.New("invalid cursor")

// Event is one audit log row as served by the API.
type Event struct {
	ID          int64           `json:"id"`
	At          time.Time       `json:"at"`
	ActorUserID *int64          `json:"actor_user_id"`
	ActorEmail  string          `json:"actor_email"`
	ProjectID   *int64          `json:"project_id"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	Detail      json.RawMessage `json:"detail"`
}

// Page is one page, newest first. NextCursor is empty on the last page.
type Page struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor"`
}

// Reader reads the audit log.
type Reader struct {
	q *sqlcgen.Queries
}

// NewReader returns a Reader on db.
func NewReader(db sqlcgen.DBTX) *Reader { return &Reader{q: sqlcgen.New(db)} }

// ListAll returns instance wide entries. The caller must have checked the
// global audit.read capability.
func (r *Reader) ListAll(ctx context.Context, cursor string, limit int) (Page, error) {
	return r.list(ctx, pgtype.Int8{}, cursor, limit)
}

// ListProject returns the entries of one project.
func (r *Reader) ListProject(ctx context.Context, scope authz.Scope, cursor string, limit int) (Page, error) {
	return r.list(ctx, pgtype.Int8{Int64: scope.ProjectID(), Valid: true}, cursor, limit)
}

func (r *Reader) list(ctx context.Context, project pgtype.Int8, cursor string, limit int) (Page, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	p := sqlcgen.ListAuditParams{ProjectID: project, PageSize: int32(limit + 1)} //nolint:gosec // bounded by MaxLimit+1
	if cursor != "" {
		id, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || id <= 0 {
			return Page{}, ErrInvalidCursor
		}
		p.BeforeID = pgtype.Int8{Int64: id, Valid: true}
	}
	rows, err := r.q.ListAudit(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list audit: %w", err)
	}
	page := Page{Items: make([]Event, 0, min(len(rows), limit))}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = strconv.FormatInt(rows[limit-1].ID, 10)
			break
		}
		page.Items = append(page.Items, Event{
			ID: row.ID, At: row.At, ActorUserID: opt(row.ActorUserID), ActorEmail: row.ActorEmail,
			ProjectID: opt(row.ProjectID), Action: row.Action, Target: row.Target, Detail: row.Detail,
		})
	}
	return page, nil
}

func opt(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
