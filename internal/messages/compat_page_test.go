package messages_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sandpost-mail/sandpost/internal/authz/authztest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store"
)

func TestListPage(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authztest.Scope(e.pid)
	ctx := context.Background()
	if err := e.repo.SetReadIDs(ctx, scope, []int64{ids[0], ids[1]}, true); err != nil {
		t.Fatal(err)
	}
	// Another project's messages never count.
	other := testProject(t, e, "other")
	addFixture(t, e, other, "simple_text.eml")

	mailbox := messages.Counts{Total: 4, Unread: 2}
	tests := []struct {
		name         string
		query        string
		start, limit int
		subjects     []string
		matches      messages.Counts
	}{
		{"everything", "", 0, 10, []string{"Nightly report", "100% off_sale", "Order shipped", "Welcome aboard"}, mailbox},
		{"paged", "", 1, 2, []string{"100% off_sale", "Order shipped"}, mailbox},
		{"filtered", "from:alice", 0, 10, []string{"100% off_sale", "Welcome aboard"}, messages.Counts{Total: 2, Unread: 1}},
		{"filtered paged", "from:alice", 1, 1, []string{"Welcome aboard"}, messages.Counts{Total: 2, Unread: 1}},
		{"no match", "subject:nothing-like-this", 0, 10, nil, messages.Counts{}},
		{"past the end", "", 50, 10, nil, mailbox},
		{"filtered past the end", "is:unread", 9, 10, nil, messages.Counts{Total: 2, Unread: 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := search.Filter{}
			if tc.query != "" {
				f = filter(t, tc.query)
			}
			page, err := e.repo.ListPage(ctx, scope, f, tc.start, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := subjectsOf(page.Rows); !slices.Equal(got, tc.subjects) {
				t.Errorf("subjects %v want %v", got, tc.subjects)
			}
			if page.Rows == nil {
				t.Error("rows must not be nil")
			}
			if page.Mailbox != mailbox || page.Matches != tc.matches {
				t.Errorf("counts: mailbox %+v matches %+v", page.Mailbox, page.Matches)
			}
		})
	}

	t.Run("same rows as ListFiltered", func(t *testing.T) {
		f := filter(t, "subject:welcome")
		want, err := e.repo.ListFiltered(ctx, scope, f, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		page, err := e.repo.ListPage(ctx, scope, f, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(page.Rows, want) {
			t.Errorf("rows differ:\n%+v\n%+v", page.Rows, want)
		}
	})

	t.Run("empty project", func(t *testing.T) {
		page, err := e.repo.ListPage(ctx, authztest.Scope(testProject(t, e, "empty")), search.Filter{}, 0, 10)
		if err != nil || len(page.Rows) != 0 || page.Mailbox != (messages.Counts{}) || page.Matches != (messages.Counts{}) {
			t.Errorf("%+v %v", page, err)
		}
	})
}

// failPrune is a store.DB whose tag pruning statement fails, inside and
// outside transactions.
type failPrune struct{ store.DB }

var errPrune = errors.New("prune failed")

func isPrune(sql string) bool {
	return strings.Contains(sql, "DELETE FROM tags") && strings.Contains(sql, "SKIP LOCKED")
}

func (d failPrune) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if isPrune(sql) {
		return pgconn.CommandTag{}, errPrune
	}
	return d.DB.Exec(ctx, sql, args...)
}

func (d failPrune) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failPruneTx{tx}, nil
}

type failPruneTx struct{ pgx.Tx }

func (t failPruneTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if isPrune(sql) {
		return pgconn.CommandTag{}, errPrune
	}
	return t.Tx.Exec(ctx, sql, args...)
}

// A failed prune must roll the delete back, never leave a committed delete
// behind an error.
func TestDeleteAndPruneAreAtomic(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authztest.Scope(e.pid)
	ctx := context.Background()
	if err := e.repo.ReplaceTags(ctx, scope, ids[:1], []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	broken := messages.New(failPrune{e.pool})
	tests := []struct {
		name string
		run  func() error
	}{
		{"by ids", func() error { _, err := broken.DeleteIDs(ctx, scope, ids[:2]); return err }},
		{"by filter", func() error { _, err := broken.DeleteFiltered(ctx, scope, filter(t, "from:alice")); return err }},
		{"all", func() error { _, err := broken.DeleteAll(ctx, scope); return err }},
		{"tag", func() error { return broken.DeleteTag(ctx, scope, "keep") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, errPrune) {
				t.Fatalf("error %v, want the prune error", err)
			}
			c, err := e.repo.CountFiltered(ctx, scope, search.Filter{})
			if err != nil || c.Total != int64(len(ids)) {
				t.Errorf("messages after a failed delete: %+v %v", c, err)
			}
			if tags, err := e.repo.TagCounts(ctx, scope); err != nil || len(tags) != 1 {
				t.Errorf("tags after a failed delete: %v %v", tags, err)
			}
		})
	}
}
