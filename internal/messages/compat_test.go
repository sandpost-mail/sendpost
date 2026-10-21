package messages_test

import (
	"context"
	"crypto/md5" //nolint:gosec // attachment checksums are shown for compatibility, not used for security
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func testProject(t *testing.T, e env, slug string) int64 {
	t.Helper()
	return testutil.Project(t, e.pool, slug)
}

// addFixture ingests a file of internal/ingest/testdata.
func addFixture(t *testing.T, e env, project int64, name string) int64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", name)) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.svc.Ingest(context.Background(), ingest.Input{ProjectID: project, EnvelopeTo: []string{"x@example.test"}, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func filter(t *testing.T, q string) search.Filter {
	t.Helper()
	f, err := search.ParseMailpit(q, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func subjectsOf(rows []messages.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Subject)
	}
	return out
}

// seedCompat stores four messages with different shapes in the project.
func seedCompat(t *testing.T, e env) []int64 {
	t.Helper()
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	return []int64{
		e.add(t, e.pid, d(10), "Alice <alice@example.test>", "bob@example.test", "Welcome aboard", "Your invoice number is 4242",
			"Cc: cc1@example.test", "Reply-To: Support <help@example.test>"),
		e.add(t, e.pid, d(11), "billing@shop.test", "carol@example.test", "Order shipped", "Tracking parcel arrives tomorrow"),
		e.add(t, e.pid, d(12), "alice@example.test", "dave@corp.test", "100% off_sale", "Unrelated text"),
		e.add(t, e.pid, d(13), "bot@example.test", "erin@example.test", "Nightly report", "all green", "Message-ID: <rep-1@example.test>"),
	}
}

func TestListFilteredSyntax(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()
	if err := e.repo.SetReadIDs(ctx, scope, []int64{ids[1]}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0], ids[2]}, []string{"billing", "run 1"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"all newest first", "", []string{"Nightly report", "100% off_sale", "Order shipped", "Welcome aboard"}},
		{"from", "from:alice", []string{"100% off_sale", "Welcome aboard"}},
		{"to", "to:carol", []string{"Order shipped"}},
		{"cc", "cc:cc1", []string{"Welcome aboard"}},
		{"bcc is the envelope minus to and cc", "bcc:bob", nil},
		{"reply-to", "reply-to:help@", []string{"Welcome aboard"}},
		{"addressed hits any address", "addressed:cc1", []string{"Welcome aboard"}},
		{"subject", "subject:ship", []string{"Order shipped"}},
		{"subject is literal for percent", "subject:100%", []string{"100% off_sale"}},
		{"message-id", "message-id:rep-1", []string{"Nightly report"}},
		{"tag", "tag:billing", []string{"100% off_sale", "Welcome aboard"}},
		{"tag with space", `tag:"run 1"`, []string{"100% off_sale", "Welcome aboard"}},
		{"tag is exact", "tag:bill", nil},
		{"is read", "is:read", []string{"Order shipped"}},
		{"is unread", "is:unread", []string{"Nightly report", "100% off_sale", "Welcome aboard"}},
		{"is tagged", "is:tagged", []string{"100% off_sale", "Welcome aboard"}},
		{"not tagged", "-is:tagged", []string{"Nightly report", "Order shipped"}},
		{"after is inclusive", "after:2026-10-12", []string{"Nightly report", "100% off_sale"}},
		{"before is inclusive", `before:"2026-10-11 12:00"`, []string{"Order shipped", "Welcome aboard"}},
		{"negated before", "-before:2026-10-12", []string{"Nightly report", "100% off_sale"}},
		{"free text in the body", "parcel", []string{"Order shipped"}},
		{"free text in the subject", "aboa", []string{"Welcome aboard"}},
		{"free text in an address", "shop.test", []string{"Order shipped"}},
		{"terms are anded", "from:alice invoice", []string{"Welcome aboard"}},
		{"negated free text", "from:alice -invoice", []string{"100% off_sale"}},
		{"repeated key is anded", "to:bob to:carol", nil},
		{"no match", "from:nobody", nil},
		{"larger than everything", "larger:10mb", nil},
		{"smaller than everything", "smaller:10mb", []string{"Nightly report", "100% off_sale", "Order shipped", "Welcome aboard"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := e.repo.ListFiltered(ctx, scope, filter(t, tc.query), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if got := subjectsOf(rows); len(got)+len(tc.want) > 0 && !slices.Equal(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
			c, err := e.repo.CountFiltered(ctx, scope, filter(t, tc.query))
			if err != nil {
				t.Fatal(err)
			}
			if int(c.Total) != len(tc.want) {
				t.Errorf("count %d want %d", c.Total, len(tc.want))
			}
		})
	}
}

func TestListFilteredAttachmentFlags(t *testing.T) {
	e := newEnv(t)
	id := addFixture(t, e, e.pid, "attachments_inline.eml")
	e.add(t, e.pid, time.Now(), "a@x.test", "b@x.test", "plain", "x")
	scope := authz.Trusted(e.pid)
	for q, want := range map[string]int{"has:attachment": 1, "has:inline": 1, "-has:attachment": 1, "": 2} {
		c, err := e.repo.CountFiltered(context.Background(), scope, filter(t, q))
		if err != nil {
			t.Fatal(err)
		}
		if int(c.Total) != want {
			t.Errorf("%q: %d want %d", q, c.Total, want)
		}
	}
	rows, err := e.repo.ListFiltered(context.Background(), scope, filter(t, "has:attachment"), 0, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].AttachmentCount != 1 {
		t.Errorf("attachment count counts files only: %+v %v", rows, err)
	}
}

func TestListFilteredRowAndPaging(t *testing.T) {
	e := newEnv(t)
	seedCompat(t, e)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()

	page, err := e.repo.ListFiltered(ctx, scope, search.Filter{}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectsOf(page); !slices.Equal(got, []string{"100% off_sale", "Order shipped"}) {
		t.Errorf("offset 1 limit 2: %v", got)
	}
	if past, _ := e.repo.ListFiltered(ctx, scope, search.Filter{}, 50, 10); len(past) != 0 {
		t.Errorf("past the end: %v", past)
	}
	if capped, _ := e.repo.ListFiltered(ctx, scope, search.Filter{}, 0, 1_000_000); len(capped) != 4 {
		t.Errorf("limit above the cap: %d", len(capped))
	}

	rows, err := e.repo.ListFiltered(ctx, scope, filter(t, "subject:welcome"), 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	r := rows[0]
	if r.Headers["Subject"][0] != "Welcome aboard" || r.TextHead == "" || r.Tags == nil {
		t.Errorf("row: %+v", r)
	}
	var kinds []string
	for _, rc := range r.Recipients {
		kinds = append(kinds, rc.Kind)
	}
	if !slices.Contains(kinds, "to") || !slices.Contains(kinds, "cc") || !slices.Contains(kinds, "envelope") {
		t.Errorf("recipient kinds: %v", kinds)
	}
}

func TestLatestID(t *testing.T) {
	e := newEnv(t)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()
	if _, err := e.repo.LatestID(ctx, scope, search.Filter{}); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("empty mailbox: %v", err)
	}
	ids := seedCompat(t, e)
	got, err := e.repo.LatestID(ctx, scope, search.Filter{})
	if err != nil || got != ids[3] {
		t.Errorf("latest = %d %v want %d", got, err, ids[3])
	}
	got, err = e.repo.LatestID(ctx, scope, filter(t, "from:alice"))
	if err != nil || got != ids[2] {
		t.Errorf("latest of a search = %d %v want %d", got, err, ids[2])
	}
}

func TestSetReadAndDelete(t *testing.T) {
	e := newEnv(t)
	other := testProject(t, e, "beta")
	ids := seedCompat(t, e)
	foreign := e.add(t, other, time.Now(), "x@x.test", "y@x.test", "foreign", "body")
	scope, otherScope := authz.Trusted(e.pid), authz.Trusted(other)
	ctx := context.Background()
	unread := func(s authz.Scope) int64 {
		c, err := e.repo.CountFiltered(ctx, s, search.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		return c.Unread
	}

	// IDs of another project are ignored.
	if err := e.repo.SetReadIDs(ctx, scope, []int64{ids[0], foreign}, true); err != nil {
		t.Fatal(err)
	}
	if unread(scope) != 3 || unread(otherScope) != 1 {
		t.Errorf("unread %d/%d after read by ids", unread(scope), unread(otherScope))
	}
	if err := e.repo.SetReadFiltered(ctx, scope, filter(t, "from:alice"), true); err != nil {
		t.Fatal(err)
	}
	if unread(scope) != 2 {
		t.Errorf("unread after read by search: %d", unread(scope))
	}
	if err := e.repo.SetReadAll(ctx, scope, false); err != nil || unread(scope) != 4 {
		t.Errorf("mark all unread: %d %v", unread(scope), err)
	}
	if err := e.repo.SetReadFiltered(ctx, scope, search.Filter{}, true); !errors.Is(err, messages.ErrEmptyFilter) {
		t.Errorf("an empty filter must be refused: %v", err)
	}
	if unread(otherScope) != 1 {
		t.Error("the other project changed")
	}

	if n, err := e.repo.DeleteIDs(ctx, scope, []int64{ids[0], foreign, 999999}); err != nil || n != 1 {
		t.Errorf("delete by ids: %d %v", n, err)
	}
	if _, err := e.repo.Get(ctx, otherScope, foreign); err != nil {
		t.Errorf("the foreign message must survive: %v", err)
	}
	if n, err := e.repo.DeleteFiltered(ctx, scope, filter(t, "from:alice")); err != nil || n != 1 {
		t.Errorf("delete by search: %d %v", n, err)
	}
	if _, err := e.repo.DeleteFiltered(ctx, scope, search.Filter{}); !errors.Is(err, messages.ErrEmptyFilter) {
		t.Errorf("an empty filter must be refused: %v", err)
	}
	if n, err := e.repo.DeleteAll(ctx, scope); err != nil || n != 2 {
		t.Errorf("delete all: %d %v", n, err)
	}
	if c, _ := e.repo.CountFiltered(ctx, otherScope, search.Filter{}); c.Total != 1 {
		t.Errorf("delete all reached another project: %d", c.Total)
	}
}

func TestTags(t *testing.T) {
	e := newEnv(t)
	other := testProject(t, e, "beta")
	ids := seedCompat(t, e)
	foreign := e.add(t, other, time.Now(), "x@x.test", "y@x.test", "foreign", "body")
	scope, otherScope := authz.Trusted(e.pid), authz.Trusted(other)
	ctx := context.Background()
	tagsOf := func(s authz.Scope) []string {
		got, err := e.repo.TagCounts(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tc := range got {
			names = append(names, tc.Name)
		}
		return names
	}

	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0], ids[1], foreign}, []string{"b", "a", "A", "bad/name", "", "  c  "}); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(scope); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("tags after set: %v (case duplicates and invalid names are dropped)", got)
	}
	if got := tagsOf(otherScope); len(got) != 0 {
		t.Errorf("a foreign message got tagged: %v", got)
	}
	d, err := e.repo.Get(ctx, scope, ids[0])
	if err != nil || !slices.Equal(d.Tags, []string{"a", "b", "c"}) {
		t.Errorf("message tags: %v %v", d.Tags, err)
	}

	// Setting replaces, an empty list clears.
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0]}, []string{"only"}); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(scope); !slices.Equal(got, []string{"a", "b", "c", "only"}) && !slices.Equal(got, []string{"a", "b", "only"}) {
		t.Errorf("tags after replace: %v", got)
	}
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0], ids[1]}, nil); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(scope); len(got) != 0 {
		t.Errorf("tags after clear (unused tags must not be listed): %v", got)
	}

	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[2]}, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RenameTag(ctx, scope, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(scope); !slices.Equal(got, []string{"new"}) {
		t.Errorf("after rename: %v", got)
	}
	if err := e.repo.RenameTag(ctx, scope, "missing", "x"); !errors.Is(err, messages.ErrTagNotFound) {
		t.Errorf("rename missing: %v", err)
	}
	if err := e.repo.RenameTag(ctx, scope, "new", "bad/name"); !errors.Is(err, messages.ErrInvalidTag) {
		t.Errorf("rename to invalid: %v", err)
	}
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[3]}, []string{"taken"}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RenameTag(ctx, scope, "new", "taken"); !errors.Is(err, messages.ErrTagExists) {
		t.Errorf("rename onto an existing tag: %v", err)
	}
	if err := e.repo.DeleteTag(ctx, scope, "taken"); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteTag(ctx, scope, "taken"); !errors.Is(err, messages.ErrTagNotFound) {
		t.Errorf("delete missing: %v", err)
	}
	if got := tagsOf(scope); !slices.Equal(got, []string{"new"}) {
		t.Errorf("after delete: %v", got)
	}
}

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"trims and dedupes without regard to case", []string{" Run 1 ", "run 1", "RUN 1"}, []string{"Run 1"}},
		{"keeps allowed punctuation", []string{"a-b_c.d@e f"}, []string{"a-b_c.d@e f"}},
		{"drops invalid and empty", []string{"", "  ", "a/b", "x<y"}, []string{}},
		{"drops too long", []string{string(make([]byte, 101))}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := messages.NormalizeTags(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAttachmentSums(t *testing.T) {
	e := newEnv(t)
	other := testProject(t, e, "beta")
	id := addFixture(t, e, e.pid, "attachments_inline.eml")
	ctx := context.Background()
	sums, err := e.repo.AttachmentSums(ctx, authz.Trusted(e.pid), id)
	if err != nil || len(sums) != 2 {
		t.Fatalf("%v %v", sums, err)
	}
	for ord, s := range sums {
		if len(s.MD5) != 32 || len(s.SHA1) != 40 || len(s.SHA256) != 64 {
			t.Errorf("part %d checksums: %+v", ord, s)
		}
	}
	// Part 0 is the PDF (attachments come before inlines), its body is base64 of this text.
	pdf := []byte("%PDF-1.4 fake pdf bytes")
	if want := fmt.Sprintf("%x", md5.Sum(pdf)); sums[0].MD5 != want { //nolint:gosec // a checksum for display, not security
		t.Errorf("md5 of part 0 = %q want %q", sums[0].MD5, want)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(pdf)); sums[0].SHA256 != want {
		t.Errorf("sha256 of part 0 = %q want %q", sums[0].SHA256, want)
	}
	if _, err := e.repo.AttachmentSums(ctx, authz.Trusted(other), id); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("foreign project: %v", err)
	}
}

func TestTagsAreCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()
	names := func() []string {
		got, err := e.repo.TagCounts(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tc := range got {
			out = append(out, tc.Name)
		}
		return out
	}

	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0]}, []string{"Foo"}); err != nil {
		t.Fatal(err)
	}
	// Another message gets the same tag in a different case: one tag, first spelling.
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[1]}, []string{"foo"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"Foo"}) {
		t.Errorf("tags: %v", got)
	}
	if c, _ := e.repo.CountFiltered(ctx, scope, filter(t, "tag:FOO")); c.Total != 2 {
		t.Errorf("tag: search is case insensitive, got %d", c.Total)
	}

	// Case only rename.
	if err := e.repo.RenameTag(ctx, scope, "Foo", "foo"); err != nil {
		t.Fatalf("case only rename: %v", err)
	}
	if got := names(); !slices.Equal(got, []string{"foo"}) {
		t.Errorf("after case rename: %v", got)
	}
	// The from tag is found without regard to case, and a case clash is a conflict.
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[2]}, []string{"Bar"}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RenameTag(ctx, scope, "FOO", "BAR"); !errors.Is(err, messages.ErrTagExists) {
		t.Errorf("rename onto a case variant: %v", err)
	}
	if err := e.repo.RenameTag(ctx, scope, "FOO", "baz"); err != nil {
		t.Errorf("rename found by another case: %v", err)
	}
	if err := e.repo.DeleteTag(ctx, scope, "BAZ"); err != nil {
		t.Errorf("delete found by another case: %v", err)
	}
	if got := names(); !slices.Equal(got, []string{"Bar"}) {
		t.Errorf("after delete: %v", got)
	}
}

func TestRenameTagEdgeCases(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()
	if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[0]}, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		from, to string
		want     error
	}{
		{"missing from, same name", "ghost", "ghost", messages.ErrTagNotFound},
		{"missing from", "ghost", "other", messages.ErrTagNotFound},
		{"invalid from is not found", "a/b", "other", messages.ErrTagNotFound},
		{"empty from is not found", "", "other", messages.ErrTagNotFound},
		{"invalid to", "keep", "a/b", messages.ErrInvalidTag},
		{"existing tag to itself", "keep", "keep", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.repo.RenameTag(ctx, scope, tc.from, tc.to); !errors.Is(err, tc.want) {
				t.Errorf("got %v want %v", err, tc.want)
			}
		})
	}
}

// Writers that set the same tags in opposite orders must not deadlock.
func TestReplaceTagsConcurrentOppositeOrders(t *testing.T) {
	e := newEnv(t)
	ids := seedCompat(t, e)
	scope := authz.Trusted(e.pid)
	ctx := context.Background()
	asc := []string{"t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08"}
	desc := slices.Clone(asc)
	slices.Reverse(desc)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 5; round++ {
				names := asc
				if i%2 == 1 {
					names = desc
				}
				// Fresh tag names each round so the upserts really insert.
				fresh := make([]string, 0, len(names))
				for _, n := range names {
					fresh = append(fresh, fmt.Sprintf("%s-%d", n, round))
				}
				if err := e.repo.ReplaceTags(ctx, scope, []int64{ids[i%len(ids)]}, fresh); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
