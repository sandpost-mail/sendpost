// Package messages is the project scoped read side of captured mail. Every
// method takes an authz.Scope and only returns rows of that project.
package messages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Page size bounds.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Domain errors.
var (
	ErrNotFound      = errors.New("message not found")
	ErrInvalidCursor = errors.New("invalid cursor")
)

// Summary is one row of the message list.
type Summary struct {
	ID              int64      `json:"id"`
	ReceivedAt      time.Time  `json:"received_at"`
	From            string     `json:"from"`
	To              []string   `json:"to"`
	Subject         string     `json:"subject"`
	SizeBytes       int64      `json:"size_bytes"`
	ReadAt          *time.Time `json:"read_at"`
	Tags            []string   `json:"tags"`
	AttachmentCount int        `json:"attachment_count"`
}

// Page is one page of results. NextCursor is empty on the last page.
type Page struct {
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor"`
}

// Recipient is one address with its kind (to, cc, bcc, envelope).
type Recipient struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

// Attachment is attachment metadata. Ordinal addresses its bytes.
type Attachment struct {
	Ordinal     int    `json:"ordinal"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentID   string `json:"content_id"`
	Inline      bool   `json:"inline"`
}

// Detail is a full message without its raw bytes.
type Detail struct {
	ID          int64               `json:"id"`
	ReceivedAt  time.Time           `json:"received_at"`
	MessageID   string              `json:"message_id"`
	From        string              `json:"from"`
	Subject     string              `json:"subject"`
	SizeBytes   int64               `json:"size_bytes"`
	Text        string              `json:"text"`
	HasHTML     bool                `json:"has_html"`
	Headers     map[string][]string `json:"headers"`
	Recipients  []Recipient         `json:"recipients"`
	Attachments []Attachment        `json:"attachments"`
	Links       []string            `json:"links"`
	Tags        []string            `json:"tags"`
	ReadAt      *time.Time          `json:"read_at"`
}

// Repository reads messages.
type Repository struct {
	q *sqlcgen.Queries
}

// New returns a Repository on db.
func New(db sqlcgen.DBTX) *Repository { return &Repository{q: sqlcgen.New(db)} }

// List returns one page of the project's messages, newest first, filtered by
// query. cursor is the NextCursor of the previous page, or empty.
func (r *Repository) List(ctx context.Context, scope authz.Scope, query search.Query, cursor string, limit int) (Page, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	params := listParams(scope.ProjectID(), query, limit+1)
	if cursor != "" {
		at, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		params.CursorAt = &at
		params.CursorID = pgtype.Int8{Int64: id, Valid: true}
	}
	rows, err := r.q.ListMessages(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list messages: %w", err)
	}
	page := Page{Items: make([]Summary, 0, min(len(rows), limit))}
	for i, row := range rows {
		if i == limit {
			last := rows[limit-1]
			page.NextCursor = encodeCursor(last.ReceivedAt, last.ID)
			break
		}
		page.Items = append(page.Items, Summary{
			ID: row.ID, ReceivedAt: row.ReceivedAt, From: row.FromAddr, To: nonNil(row.ToAddrs),
			Subject: row.Subject, SizeBytes: row.SizeBytes, ReadAt: row.ReadAt,
			Tags: nonNil(row.TagNames), AttachmentCount: int(row.AttachmentCount),
		})
	}
	return page, nil
}

func listParams(projectID int64, q search.Query, pageSize int) sqlcgen.ListMessagesParams {
	p := sqlcgen.ListMessagesParams{
		ProjectID:      projectID,
		FromLike:       text(search.LikePattern(q.From)),
		ToLike:         text(search.LikePattern(q.To)),
		SubjectLike:    text(search.LikePattern(q.Subject)),
		Tag:            text(q.Tag),
		Body:           text(q.Body),
		Free:           text(q.Free),
		FreeLike:       text(search.LikePattern(strings.ReplaceAll(q.Free, `"`, ""))),
		ReceivedAfter:  q.After,
		ReceivedBefore: q.Before,
		PageSize:       int32(pageSize), //nolint:gosec // bounded by MaxLimit+1
	}
	return p
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Get returns a message with its recipients, attachments, links and tags.
func (r *Repository) Get(ctx context.Context, scope authz.Scope, id int64) (Detail, error) {
	m, err := r.q.GetMessage(ctx, sqlcgen.GetMessageParams{ProjectID: scope.ProjectID(), ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("get message: %w", err)
	}
	headers := map[string][]string{}
	if err := json.Unmarshal(m.Headers, &headers); err != nil {
		return Detail{}, fmt.Errorf("decode headers: %w", err)
	}
	d := Detail{
		ID: m.ID, ReceivedAt: m.ReceivedAt, MessageID: m.MessageIDHdr, From: m.FromAddr,
		Subject: m.Subject, SizeBytes: m.SizeBytes, Text: m.TextBody, HasHTML: m.HtmlBody != "",
		Headers: headers, ReadAt: m.ReadAt,
	}
	if d.Recipients, err = r.recipients(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Attachments, err = r.attachments(ctx, id); err != nil {
		return Detail{}, err
	}
	if d.Links, err = r.q.ListMessageLinks(ctx, id); err != nil {
		return Detail{}, fmt.Errorf("list links: %w", err)
	}
	if d.Tags, err = r.q.ListMessageTagNames(ctx, id); err != nil {
		return Detail{}, fmt.Errorf("list tags: %w", err)
	}
	d.Links, d.Tags = nonNil(d.Links), nonNil(d.Tags)
	return d, nil
}

func (r *Repository) recipients(ctx context.Context, id int64) ([]Recipient, error) {
	rows, err := r.q.ListMessageRecipients(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list recipients: %w", err)
	}
	out := make([]Recipient, 0, len(rows))
	for _, row := range rows {
		out = append(out, Recipient{Kind: row.Kind, Address: row.Address})
	}
	return out, nil
}

func (r *Repository) attachments(ctx context.Context, id int64) ([]Attachment, error) {
	rows, err := r.q.ListMessageAttachments(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	out := make([]Attachment, 0, len(rows))
	for _, row := range rows {
		out = append(out, Attachment{
			Ordinal: int(row.Ordinal), Filename: row.Filename, ContentType: row.ContentType,
			SizeBytes: row.SizeBytes, ContentID: row.ContentID, Inline: row.Inline,
		})
	}
	return out, nil
}

// Raw returns the original message bytes.
func (r *Repository) Raw(ctx context.Context, scope authz.Scope, id int64) ([]byte, error) {
	raw, err := r.q.GetMessageRaw(ctx, sqlcgen.GetMessageRawParams{ProjectID: scope.ProjectID(), ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get raw: %w", err)
	}
	return raw, nil
}

// HTML returns the HTML body, "" when the message has none.
func (r *Repository) HTML(ctx context.Context, scope authz.Scope, id int64) (string, error) {
	html, err := r.q.GetMessageHTML(ctx, sqlcgen.GetMessageHTMLParams{ProjectID: scope.ProjectID(), ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get html: %w", err)
	}
	return html, nil
}

// AttachmentContent returns the metadata and decoded bytes of one attachment,
// read back out of the stored raw message.
func (r *Repository) AttachmentContent(ctx context.Context, scope authz.Scope, id int64, ordinal int) (Attachment, []byte, error) {
	raw, err := r.Raw(ctx, scope, id)
	if err != nil {
		return Attachment{}, nil, err
	}
	a, content, ok := ingest.AttachmentContent(raw, ordinal)
	if !ok {
		return Attachment{}, nil, ErrNotFound
	}
	return Attachment{
		Ordinal: a.Ordinal, Filename: a.Filename, ContentType: a.ContentType,
		SizeBytes: a.Size, ContentID: a.ContentID, Inline: a.Inline,
	}, content, nil
}

// MarkRead sets read_at on the first view. It is a no-op when already read and
// ErrNotFound when the message is not in the project.
func (r *Repository) MarkRead(ctx context.Context, scope authz.Scope, id int64) error {
	p := sqlcgen.MarkMessageReadParams{ProjectID: scope.ProjectID(), ID: id}
	n, err := r.q.MarkMessageRead(ctx, p)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	if n > 0 {
		return nil
	}
	exists, err := r.q.MessageExists(ctx, sqlcgen.MessageExistsParams(p))
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func encodeCursor(at time.Time, id int64) string {
	raw := strconv.FormatInt(at.UnixMicro(), 10) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	us, ids, ok := strings.Cut(string(raw), ":")
	if !ok {
		return time.Time{}, 0, ErrInvalidCursor
	}
	micro, err1 := strconv.ParseInt(us, 10, 64)
	id, err2 := strconv.ParseInt(ids, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return time.UnixMicro(micro).UTC(), id, nil
}
