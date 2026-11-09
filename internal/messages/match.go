package messages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// MaxRunIDLength bounds a run id in bytes.
const MaxRunIDLength = 100

// ErrInvalidRun is returned for an malformed or too long run id.
var ErrInvalidRun = errors.New("invalid run id")

// MatchQuery selects messages for the wait endpoint.
type MatchQuery struct {
	// Query is the usual search syntax.
	Query search.Query
	// Run isolates one CI run: only messages carrying the header X-Run-Id
	// with this value, or tagged with it, match. Empty means no isolation.
	Run string
	// AfterID skips messages with an id up to and including it.
	AfterID int64
	// Limit caps the result, DefaultLimit when 0, MaxLimit at most.
	Limit int
}

// ValidateRunID checks a run id: 1 to MaxRunIDLength bytes of printable text.
// The empty string is valid and means "no run".
func ValidateRunID(run string) error {
	if run == "" {
		return nil
	}
	if len(run) > MaxRunIDLength || !utf8.ValidString(run) || strings.TrimSpace(run) != run {
		return ErrInvalidRun
	}
	for _, r := range run {
		if unicode.IsControl(r) {
			return ErrInvalidRun
		}
	}
	return nil
}

// Match returns the project's messages matching q, oldest first. It never
// returns a row of another project.
func (r *Repository) Match(ctx context.Context, scope authz.Scope, q MatchQuery) ([]Summary, error) {
	if err := ValidateRunID(q.Run); err != nil {
		return nil, err
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	lp := listParams(scope.ProjectID(), q.Query, limit)
	rows, err := r.q.MatchMessages(ctx, sqlcgen.MatchMessagesParams{
		ProjectID: lp.ProjectID, AfterID: max(q.AfterID, 0),
		FromLike: lp.FromLike, ToLike: lp.ToLike, SubjectLike: lp.SubjectLike, Tag: lp.Tag,
		Run: text(q.Run), ReceivedAfter: lp.ReceivedAfter, ReceivedBefore: lp.ReceivedBefore,
		Body: lp.Body, Free: lp.Free, FreeLike: lp.FreeLike, PageSize: lp.PageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("match messages: %w", err)
	}
	out := make([]Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, Summary{
			ID: row.ID, ReceivedAt: row.ReceivedAt, From: row.FromAddr, To: nonNil(row.ToAddrs),
			Subject: row.Subject, SizeBytes: row.SizeBytes, ReadAt: row.ReadAt,
			Tags: nonNil(row.TagNames), AttachmentCount: int(row.AttachmentCount),
		})
	}
	return out, nil
}
