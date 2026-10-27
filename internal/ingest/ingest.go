// Package ingest turns a raw SMTP message into stored rows: it parses the
// MIME structure, inserts everything in one transaction, applies the project's
// tag rules and publishes a LISTEN/NOTIFY event on commit.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// ErrProjectNotFound is returned when the target project does not exist.
var ErrProjectNotFound = errors.New("project not found")

// TxBeginner is satisfied by *pgxpool.Pool.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Input is one message as received over SMTP.
type Input struct {
	ProjectID    int64
	EnvelopeFrom string
	EnvelopeTo   []string
	Raw          []byte
}

// Hook runs inside the ingest transaction, after the message rows exist. The
// delivery package uses it to queue the policy run in the same transaction,
// so a committed message always has its job. A failing hook never fails the
// capture: it runs in a savepoint that is rolled back, the message is stored
// and the error is logged.
type Hook interface {
	AfterInsert(ctx context.Context, tx pgx.Tx, projectID, messageID int64) error
}

// Service ingests messages.
type Service struct {
	db   TxBeginner
	now  func() time.Time
	hook Hook
	log  *slog.Logger
}

// NewService returns a Service storing into db.
func NewService(db TxBeginner) *Service {
	return &Service{db: db, now: time.Now, log: slog.New(slog.DiscardHandler)}
}

// WithHook returns a copy of s that calls h inside every ingest transaction.
// Failures are logged to log (nil discards them).
func (s *Service) WithHook(h Hook, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := *s
	c.hook, c.log = h, log
	return &c
}

// runHook calls the hook in a savepoint. Nothing it does or fails at can
// affect the capture.
func (s *Service) runHook(ctx context.Context, tx pgx.Tx, projectID, messageID int64) {
	if s.hook == nil {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		s.log.Error("ingest hook: savepoint", "project_id", projectID, "message_id", messageID, "error", err)
		return
	}
	if err := s.hook.AfterInsert(ctx, sp, projectID, messageID); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		s.log.Error("ingest hook failed, the message is stored without its delivery job",
			"project_id", projectID, "message_id", messageID, "error", err)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		s.log.Error("ingest hook: release savepoint", "project_id", projectID, "message_id", messageID, "error", err)
	}
}

// Ingest parses and stores a message and returns its id. Parsing never fails,
// so a message that arrives is always captured.
func (s *Service) Ingest(ctx context.Context, in Input) (int64, error) {
	if in.Raw == nil {
		in.Raw = []byte{} // the column is NOT NULL
	}
	parsed := Parse(in.Raw, in.EnvelopeFrom, in.EnvelopeTo)
	headers, err := json.Marshal(parsed.Headers)
	if err != nil {
		return 0, fmt.Errorf("encode headers: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := sqlcgen.New(tx)
	row, err := q.InsertMessage(ctx, sqlcgen.InsertMessageParams{
		ProjectID:    in.ProjectID,
		ReceivedAt:   s.now().UTC(),
		MessageIDHdr: parsed.MessageID,
		FromAddr:     parsed.From,
		Subject:      parsed.Subject,
		SizeBytes:    int64(len(in.Raw)),
		TextBody:     parsed.Text,
		HtmlBody:     parsed.HTML,
		Raw:          in.Raw,
		Headers:      headers,
	})
	if store.IsForeignKeyViolation(err) {
		return 0, ErrProjectNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	if err := insertChildren(ctx, q, row.ID, parsed); err != nil {
		return 0, err
	}
	if err := applyTagRules(ctx, q, in.ProjectID, row.ID, parsed); err != nil {
		return 0, err
	}
	s.runHook(ctx, tx, in.ProjectID, row.ID)
	ev := events.Event{Type: events.TypeMessageCreated, MessageID: row.ID}
	if err := events.Notify(ctx, tx, in.ProjectID, ev); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return row.ID, nil
}

func insertChildren(ctx context.Context, q *sqlcgen.Queries, id int64, p Parsed) error {
	rcpts := make([]sqlcgen.InsertRecipientsParams, 0, len(p.Recipients))
	for _, r := range p.Recipients {
		rcpts = append(rcpts, sqlcgen.InsertRecipientsParams{MessageID: id, Kind: r.Kind, Address: r.Address})
	}
	if _, err := q.InsertRecipients(ctx, rcpts); err != nil {
		return fmt.Errorf("insert recipients: %w", err)
	}
	atts := make([]sqlcgen.InsertAttachmentsParams, 0, len(p.Attachments))
	for _, a := range p.Attachments {
		atts = append(atts, sqlcgen.InsertAttachmentsParams{
			MessageID: id, Ordinal: int32(a.Ordinal), Filename: a.Filename, //nolint:gosec // bounded by maxParsedParts
			ContentType: a.ContentType, SizeBytes: a.Size, ContentID: a.ContentID, Inline: a.Inline,
		})
	}
	if _, err := q.InsertAttachments(ctx, atts); err != nil {
		return fmt.Errorf("insert attachments: %w", err)
	}
	links := make([]sqlcgen.InsertLinksParams, 0, len(p.Links))
	for i, u := range p.Links {
		links = append(links, sqlcgen.InsertLinksParams{MessageID: id, Position: int32(i), Url: u}) //nolint:gosec // bounded by maxLinks
	}
	if _, err := q.InsertLinks(ctx, links); err != nil {
		return fmt.Errorf("insert links: %w", err)
	}
	return nil
}

func applyTagRules(ctx context.Context, q *sqlcgen.Queries, projectID, messageID int64, p Parsed) error {
	stored, err := q.ListTagRules(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list tag rules: %w", err)
	}
	if len(stored) == 0 {
		return nil
	}
	rules := make([]Rule, 0, len(stored))
	for _, r := range stored {
		rules = append(rules, Rule{Kind: r.Kind, HeaderName: r.HeaderName, Pattern: r.Pattern, Tag: r.Tag})
	}
	names := ApplyRules(rules, p.Headers, p.Recipients)
	if len(names) == 0 {
		return nil
	}
	ids, err := q.UpsertTags(ctx, sqlcgen.UpsertTagsParams{ProjectID: projectID, Names: names})
	if err != nil {
		return fmt.Errorf("upsert tags: %w", err)
	}
	if err := q.AttachTags(ctx, sqlcgen.AttachTagsParams{MessageID: messageID, TagIds: ids}); err != nil {
		return fmt.Errorf("attach tags: %w", err)
	}
	return nil
}
