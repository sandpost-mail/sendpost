// Package comments is the project scoped store of comments on messages. Every
// method takes an authz.Scope and restricts project_id in SQL, so a message
// or comment of another project behaves like an unknown one. A comment is
// plain text: it is validated here and escaped by the client when rendered,
// never stored or served as HTML.
package comments

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Limits.
const (
	// MaxBodyRunes is the longest comment, in characters (the web UI uses the same value).
	MaxBodyRunes = 2000
	DefaultLimit = 100
	MaxLimit     = 200
)

// Domain errors. ErrInvalid messages are safe to show.
var (
	ErrInvalid       = errors.New("invalid comment")
	ErrInvalidCursor = errors.New("invalid cursor")
)

// Comment is one comment as served by the API.
type Comment struct {
	ID        int64     `json:"id"`
	MessageID int64     `json:"message_id"`
	UserID    *int64    `json:"user_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Page is a page of comments, oldest first. NextCursor is empty on the last page.
type Page struct {
	Items      []Comment `json:"items"`
	NextCursor string    `json:"next_cursor"`
}

// Author says who writes: a user (session or user token), or a project token.
type Author struct {
	UserID  int64
	TokenID int64
}

// Service reads and writes comments.
type Service struct {
	db store.DB
	q  *sqlcgen.Queries
}

// New returns a Service on db.
func New(db store.DB) *Service { return &Service{db: db, q: sqlcgen.New(db)} }

// Normalize validates a comment body and returns it trimmed. The text is kept
// as typed otherwise: line breaks and tabs are allowed, other control
// characters (including NUL, which Postgres rejects) and invalid UTF-8 are not.
func Normalize(body string) (string, error) {
	if !utf8.ValidString(body) {
		return "", fmt.Errorf("%w: text must be valid UTF-8", ErrInvalid)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("%w: text is required", ErrInvalid)
	}
	if utf8.RuneCountInString(body) > MaxBodyRunes {
		return "", fmt.Errorf("%w: text is longer than %d characters", ErrInvalid, MaxBodyRunes)
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return "", fmt.Errorf("%w: text contains control characters", ErrInvalid)
		}
	}
	return body, nil
}

// Create adds a comment to a message of the project. A message that is not in
// the project returns messages.ErrNotFound. The row, its audit entry and the
// event commit together, so subscribers hear about it only if it exists.
func (s *Service) Create(ctx context.Context, scope authz.Scope, messageID int64, by Author, body string) (Comment, error) {
	// Existence first: a message of another project answers not found whatever
	// the body is, so the validation message can not be used to probe ids.
	ok, err := s.q.MessageExists(ctx, sqlcgen.MessageExistsParams{ProjectID: scope.ProjectID(), ID: messageID})
	if err != nil {
		return Comment{}, fmt.Errorf("check message: %w", err)
	}
	if !ok {
		return Comment{}, messages.ErrNotFound
	}
	body, err = Normalize(body)
	if err != nil {
		return Comment{}, err
	}
	var out Comment
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, err := q.InsertComment(ctx, sqlcgen.InsertCommentParams{
			ProjectID: scope.ProjectID(), MessageID: messageID, Body: body,
			UserID:  pgtype.Int8{Int64: by.UserID, Valid: by.UserID != 0},
			TokenID: pgtype.Int8{Int64: by.TokenID, Valid: by.TokenID != 0},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return messages.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}
		out = toComment(row.ID, row.MessageID, row.UserID, row.AuthorLabel, row.Body, row.CreatedAt)
		if err := audit.Record(ctx, q, audit.Entry{
			Action: "comment.create", ProjectID: scope.ProjectID(), Target: fmt.Sprintf("message:%d", messageID),
			Detail: map[string]any{"comment_id": row.ID},
		}); err != nil {
			return err
		}
		return events.Notify(ctx, tx, scope.ProjectID(), events.Event{Type: events.TypeCommentCreated, MessageID: messageID})
	})
	return out, err
}

// List returns the comments of a message, oldest first. cursor is the
// NextCursor of the previous page.
func (s *Service) List(ctx context.Context, scope authz.Scope, messageID int64, cursor string, limit int) (Page, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	var after int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n <= 0 {
			return Page{}, ErrInvalidCursor
		}
		after = n
	}
	ok, err := s.q.MessageExists(ctx, sqlcgen.MessageExistsParams{ProjectID: scope.ProjectID(), ID: messageID})
	if err != nil {
		return Page{}, fmt.Errorf("check message: %w", err)
	}
	if !ok {
		return Page{}, messages.ErrNotFound
	}
	rows, err := s.q.ListComments(ctx, sqlcgen.ListCommentsParams{
		ProjectID: scope.ProjectID(), MessageID: messageID, AfterID: after, PageSize: int32(limit + 1), //nolint:gosec // bounded
	})
	if err != nil {
		return Page{}, fmt.Errorf("list comments: %w", err)
	}
	page := Page{Items: make([]Comment, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			page.NextCursor = strconv.FormatInt(rows[limit-1].ID, 10)
			break
		}
		page.Items = append(page.Items, toComment(r.ID, r.MessageID, r.UserID, r.AuthorLabel, r.Body, r.CreatedAt))
	}
	return page, nil
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func toComment(id, messageID int64, user pgtype.Int8, author, body string, at time.Time) Comment {
	c := Comment{ID: id, MessageID: messageID, Author: author, Body: body, CreatedAt: at}
	if user.Valid {
		c.UserID = &user.Int64
	}
	return c
}
