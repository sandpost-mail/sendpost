package messages

import (
	"context"
	"crypto/md5"  //nolint:gosec // attachment checksums are shown for compatibility, not used for security
	"crypto/sha1" //nolint:gosec // same: Mailpit reports MD5 and SHA1 next to SHA256
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// MaxCompatLimit caps a page of the offset based listing.
const MaxCompatLimit = 1000

// Errors of the offset based listing and of tag and bulk operations.
var (
	// ErrEmptyFilter is returned when a bulk write by search gets a filter
	// without terms, which would select every message. Use the explicit
	// "all" variants for that.
	ErrEmptyFilter = errors.New("search has no usable term")
	ErrTagNotFound = errors.New("tag not found")
	ErrTagExists   = errors.New("tag already exists")
	ErrInvalidTag  = errors.New("invalid tag name")
)

// validTag is the tag syntax of Mailpit: letters, digits, space and - _ . @.
var validTag = regexp.MustCompile(`^[a-zA-Z0-9\-_ .@]{1,100}$`)

// Row is a message of a filtered listing with what the summary of the
// Mailpit API needs: the stored headers (for display names and Reply-To), all
// recipients by kind and the start of the text.
type Row struct {
	ID              int64
	ReceivedAt      time.Time
	MessageID       string
	From            string
	Subject         string
	SizeBytes       int64
	ReadAt          *time.Time
	Headers         map[string][]string
	Recipients      []Recipient
	Tags            []string
	AttachmentCount int
	// TextHead is the first part of the text body. HTMLHead is the first part
	// of the HTML body and only set when there is no text body.
	TextHead string
	HTMLHead string
}

// Counts are the number of messages matching a filter and how many are unread.
type Counts struct {
	Total  int64
	Unread int64
}

// TagCount is a tag with the number of messages that carry it.
type TagCount struct {
	Name     string
	Messages int64
}

// Checksums are the digests of one attachment.
type Checksums struct {
	MD5    string
	SHA1   string
	SHA256 string
}

// filterArgs returns the three parallel arrays of f, never nil (a nil array is
// a SQL NULL).
func filterArgs(f search.Filter) (kinds, vals []string, negs []bool) {
	kinds, vals, negs = f.Kinds, f.Vals, f.Negs
	if kinds == nil {
		kinds, vals, negs = []string{}, []string{}, []bool{}
	}
	return kinds, vals, negs
}

// ListFiltered returns messages matching f, newest first, from offset start.
// limit is clamped to 1..MaxCompatLimit.
func (r *Repository) ListFiltered(ctx context.Context, scope authz.Scope, f search.Filter, start, limit int) ([]Row, error) {
	limit = min(max(limit, 1), MaxCompatLimit)
	kinds, vals, negs := filterArgs(f)
	rows, err := r.q.CompatListMessages(ctx, sqlcgen.CompatListMessagesParams{
		ProjectID: scope.ProjectID(), Kinds: kinds, Vals: vals, Negs: negs,
		RowOffset: int32(min(max(start, 0), 1<<30)), //nolint:gosec // clamped
		RowLimit:  int32(limit),                     //nolint:gosec // clamped
	})
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		item := Row{
			ID: row.ID, ReceivedAt: row.ReceivedAt, MessageID: row.MessageIDHdr, From: row.FromAddr,
			Subject: row.Subject, SizeBytes: row.SizeBytes, ReadAt: row.ReadAt, Tags: nonNil(row.TagNames),
			AttachmentCount: int(row.AttachmentCount), TextHead: row.TextHead, HTMLHead: row.HtmlHead,
		}
		if err := json.Unmarshal(row.Headers, &item.Headers); err != nil {
			return nil, fmt.Errorf("decode headers: %w", err)
		}
		if err := json.Unmarshal(row.Recipients, &item.Recipients); err != nil {
			return nil, fmt.Errorf("decode recipients: %w", err)
		}
		out = append(out, item)
	}
	return out, nil
}

// CountFiltered counts the messages matching f and how many of them are unread.
func (r *Repository) CountFiltered(ctx context.Context, scope authz.Scope, f search.Filter) (Counts, error) {
	kinds, vals, negs := filterArgs(f)
	row, err := r.q.CompatCountMessages(ctx, sqlcgen.CompatCountMessagesParams{
		ProjectID: scope.ProjectID(), Kinds: kinds, Vals: vals, Negs: negs,
	})
	if err != nil {
		return Counts{}, fmt.Errorf("count messages: %w", err)
	}
	return Counts{Total: row.Total, Unread: row.Unread}, nil
}

// LatestID returns the id of the newest message matching f, or ErrNotFound.
func (r *Repository) LatestID(ctx context.Context, scope authz.Scope, f search.Filter) (int64, error) {
	rows, err := r.ListFiltered(ctx, scope, f, 0, 1)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, ErrNotFound
	}
	return rows[0].ID, nil
}

// DeleteIDs deletes the given messages of the project and returns how many
// existed. Ids of other projects are ignored like unknown ones.
func (r *Repository) DeleteIDs(ctx context.Context, scope authz.Scope, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := r.q.CompatDeleteByIDs(ctx, sqlcgen.CompatDeleteByIDsParams{ProjectID: scope.ProjectID(), Ids: ids})
	if err != nil {
		return 0, fmt.Errorf("delete messages: %w", err)
	}
	return n, r.prune(ctx, scope, n)
}

// DeleteFiltered deletes the messages matching f. A filter without terms is
// refused with ErrEmptyFilter.
func (r *Repository) DeleteFiltered(ctx context.Context, scope authz.Scope, f search.Filter) (int64, error) {
	if f.IsZero() {
		return 0, ErrEmptyFilter
	}
	return r.deleteMatching(ctx, scope, f)
}

// DeleteAll deletes every message of the project.
func (r *Repository) DeleteAll(ctx context.Context, scope authz.Scope) (int64, error) {
	return r.deleteMatching(ctx, scope, search.Filter{})
}

func (r *Repository) deleteMatching(ctx context.Context, scope authz.Scope, f search.Filter) (int64, error) {
	kinds, vals, negs := filterArgs(f)
	n, err := r.q.CompatDeleteMatching(ctx, sqlcgen.CompatDeleteMatchingParams{
		ProjectID: scope.ProjectID(), Kinds: kinds, Vals: vals, Negs: negs,
	})
	if err != nil {
		return 0, fmt.Errorf("delete messages: %w", err)
	}
	return n, r.prune(ctx, scope, n)
}

// prune drops tags that no message carries any more, after messages went away.
func (r *Repository) prune(ctx context.Context, scope authz.Scope, deleted int64) error {
	if deleted == 0 {
		return nil
	}
	if err := r.q.CompatPruneTags(ctx, scope.ProjectID()); err != nil {
		return fmt.Errorf("prune tags: %w", err)
	}
	return nil
}

// SetReadIDs marks the given messages read or unread. Unknown ids and ids of
// other projects are ignored.
func (r *Repository) SetReadIDs(ctx context.Context, scope authz.Scope, ids []int64, read bool) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.q.CompatSetReadByIDs(ctx, sqlcgen.CompatSetReadByIDsParams{ProjectID: scope.ProjectID(), Ids: ids, Read: read})
	if err != nil {
		return fmt.Errorf("set read: %w", err)
	}
	return nil
}

// SetReadFiltered marks the messages matching f. A filter without terms is
// refused with ErrEmptyFilter.
func (r *Repository) SetReadFiltered(ctx context.Context, scope authz.Scope, f search.Filter, read bool) error {
	if f.IsZero() {
		return ErrEmptyFilter
	}
	return r.setReadMatching(ctx, scope, f, read)
}

// SetReadAll marks every message of the project.
func (r *Repository) SetReadAll(ctx context.Context, scope authz.Scope, read bool) error {
	return r.setReadMatching(ctx, scope, search.Filter{}, read)
}

func (r *Repository) setReadMatching(ctx context.Context, scope authz.Scope, f search.Filter, read bool) error {
	kinds, vals, negs := filterArgs(f)
	_, err := r.q.CompatSetReadMatching(ctx, sqlcgen.CompatSetReadMatchingParams{
		ProjectID: scope.ProjectID(), Kinds: kinds, Vals: vals, Negs: negs, Read: read,
	})
	if err != nil {
		return fmt.Errorf("set read: %w", err)
	}
	return nil
}

// NormalizeTags trims names, drops the ones that are not valid tags and
// removes duplicates without regard to case (the first spelling wins). The
// result is never nil.
func NormalizeTags(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !validTag.MatchString(n) {
			continue
		}
		key := strings.ToLower(n)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, n)
	}
	return out
}

// ReplaceTags sets the tags of the given messages to names, removing the ones
// they had. Names that are not valid tags are dropped, ids of other projects
// are ignored, and an empty list clears the tags. It runs in one transaction.
func (r *Repository) ReplaceTags(ctx context.Context, scope authz.Scope, ids []int64, names []string) error {
	if len(ids) == 0 {
		return nil
	}
	names = NormalizeTags(names)
	// Sorted, so concurrent writers take tag locks in the same order.
	slices.SortFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	pid := scope.ProjectID()
	return store.InTx(ctx, r.db, func(q *sqlcgen.Queries) error {
		own, err := q.CompatProjectMessageIDs(ctx, sqlcgen.CompatProjectMessageIDsParams{ProjectID: pid, Ids: ids})
		if err != nil {
			return fmt.Errorf("resolve messages: %w", err)
		}
		if len(own) == 0 {
			return nil
		}
		if err := q.CompatClearMessageTags(ctx, sqlcgen.CompatClearMessageTagsParams{ProjectID: pid, Ids: own}); err != nil {
			return fmt.Errorf("clear tags: %w", err)
		}
		if len(names) > 0 {
			tagIDs, err := q.UpsertTags(ctx, sqlcgen.UpsertTagsParams{ProjectID: pid, Names: names})
			if err != nil {
				return fmt.Errorf("upsert tags: %w", err)
			}
			if err := q.CompatAttachTags(ctx, sqlcgen.CompatAttachTagsParams{ProjectID: pid, Ids: own, TagIds: tagIDs}); err != nil {
				return fmt.Errorf("attach tags: %w", err)
			}
		}
		if err := q.CompatPruneTags(ctx, pid); err != nil {
			return fmt.Errorf("prune tags: %w", err)
		}
		return nil
	})
}

// TagCounts lists the tags in use in the project with their message counts.
func (r *Repository) TagCounts(ctx context.Context, scope authz.Scope) ([]TagCount, error) {
	rows, err := r.q.CompatListTags(ctx, scope.ProjectID())
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	out := make([]TagCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, TagCount{Name: row.Name, Messages: row.Messages})
	}
	return out, nil
}

// RenameTag renames a tag of the project. Tag names are case insensitive,
// so from is found without regard to case and a case only rename is allowed.
// It returns ErrTagNotFound (also when from equals to but does not exist),
// ErrTagExists when another tag already has that name, or ErrInvalidTag.
func (r *Repository) RenameTag(ctx context.Context, scope authz.Scope, from, to string) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if !validTag.MatchString(from) {
		return ErrTagNotFound
	}
	if !validTag.MatchString(to) {
		return ErrInvalidTag
	}
	pid := scope.ProjectID()
	return store.InTx(ctx, r.db, func(q *sqlcgen.Queries) error {
		// An unused tag row of that name must not block the rename.
		if err := q.CompatPruneTags(ctx, pid); err != nil {
			return fmt.Errorf("prune tags: %w", err)
		}
		n, err := q.CompatRenameTag(ctx, sqlcgen.CompatRenameTagParams{ProjectID: pid, OldName: from, NewName: to})
		if store.IsUniqueViolation(err) {
			return ErrTagExists
		}
		if err != nil {
			return fmt.Errorf("rename tag: %w", err)
		}
		if n == 0 {
			return ErrTagNotFound
		}
		return nil
	})
}

// DeleteTag removes a tag from every message of the project. It returns
// ErrTagNotFound when no message has it.
func (r *Repository) DeleteTag(ctx context.Context, scope authz.Scope, name string) error {
	if err := r.q.CompatPruneTags(ctx, scope.ProjectID()); err != nil {
		return fmt.Errorf("prune tags: %w", err)
	}
	n, err := r.q.CompatDeleteTag(ctx, sqlcgen.CompatDeleteTagParams{ProjectID: scope.ProjectID(), Name: name})
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if n == 0 {
		return ErrTagNotFound
	}
	return nil
}

// AttachmentSums returns the digests of every attachment of a message by
// ordinal. It re-parses the stored raw message, so call it only when the
// message has attachments.
func (r *Repository) AttachmentSums(ctx context.Context, scope authz.Scope, id int64) (map[int]Checksums, error) {
	raw, err := r.Raw(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	parts := ingest.AttachmentBytes(raw)
	out := make(map[int]Checksums, len(parts))
	for i, content := range parts {
		m, s1, s256 := md5.Sum(content), sha1.Sum(content), sha256.Sum256(content) //nolint:gosec // see imports
		out[i] = Checksums{MD5: hex.EncodeToString(m[:]), SHA1: hex.EncodeToString(s1[:]), SHA256: hex.EncodeToString(s256[:])}
	}
	return out, nil
}
