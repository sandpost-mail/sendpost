// Package activity serves the project activity feed: what happened lately in
// a project, merged from received messages, comments and manual releases.
// It reads those sources explicitly and never the whole audit log, which holds
// owner only details (transport, policy, credentials).
package activity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// Kinds of activity.
const (
	KindMessage = "message"
	KindComment = "comment"
	KindRelease = "release"
)

// Item is one entry of the feed. Actor is the sender of a message, the author
// of a comment or the user who released. Detail is a comment excerpt (200
// characters at most) or the release targets, empty for messages.
type Item struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	ID        int64     `json:"id"`
	MessageID int64     `json:"message_id"`
	Subject   string    `json:"subject"`
	Actor     string    `json:"actor"`
	Detail    string    `json:"detail"`
}

// Page is one page, newest first. NextCursor is empty on the last page.
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// Reader reads the feed.
type Reader struct{ q *sqlcgen.Queries }

// NewReader returns a Reader on db.
func NewReader(db sqlcgen.DBTX) *Reader { return &Reader{q: sqlcgen.New(db)} }

// List returns one page of the project's activity.
func (r *Reader) List(ctx context.Context, scope authz.Scope, cursor string, limit int) (Page, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	p := sqlcgen.ListActivityParams{ProjectID: scope.ProjectID(), PageSize: int32(limit + 1)} //nolint:gosec // bounded
	if cursor != "" {
		at, kind, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		p.CursorAt = &at
		p.CursorKind = pgtype.Text{String: kind, Valid: true}
		p.CursorID = pgtype.Int8{Int64: id, Valid: true}
	}
	rows, err := r.q.ListActivity(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list activity: %w", err)
	}
	page := Page{Items: make([]Item, 0, min(len(rows), limit))}
	for i, row := range rows {
		if i == limit {
			last := rows[limit-1]
			page.NextCursor = encodeCursor(last.At, last.Kind, last.ID)
			break
		}
		page.Items = append(page.Items, Item{
			At: row.At, Kind: row.Kind, ID: row.ID, MessageID: row.MessageID,
			Subject: row.Subject, Actor: row.Actor, Detail: row.Detail,
		})
	}
	return page, nil
}

func encodeCursor(at time.Time, kind string, id int64) string {
	raw := strconv.FormatInt(at.UnixMicro(), 10) + "|" + kind + "|" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string) (time.Time, string, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", 0, ErrInvalidCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return time.Time{}, "", 0, ErrInvalidCursor
	}
	micro, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || id <= 0 {
		return time.Time{}, "", 0, ErrInvalidCursor
	}
	switch parts[1] {
	case KindMessage, KindComment, KindRelease:
	default:
		return time.Time{}, "", 0, ErrInvalidCursor
	}
	return time.UnixMicro(micro).UTC(), parts[1], id, nil
}
