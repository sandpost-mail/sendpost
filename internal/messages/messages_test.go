package messages_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

type env struct {
	pool *pgxpool.Pool
	svc  *ingest.Service
	repo *messages.Repository
	pid  int64
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testutil.Pool(t)
	return env{pool: pool, svc: ingest.NewService(pool), repo: messages.New(pool), pid: testutil.Project(t, pool, "alpha")}
}

// add stores a message and back-dates it so ordering is deterministic.
func (e env) add(t *testing.T, project int64, at time.Time, from, to, subject, body string, extraHeaders ...string) int64 {
	t.Helper()
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n%s\r\n%s", from, to, subject,
		strings.Join(append(extraHeaders, ""), "\r\n"), body)
	id, err := e.svc.Ingest(context.Background(), ingest.Input{ProjectID: project, EnvelopeTo: []string{to}, Raw: []byte(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), "UPDATE messages SET received_at=$1 WHERE id=$2", at, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func list(t *testing.T, e env, project int64, q string) []string {
	t.Helper()
	query, err := search.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	page, err := e.repo.List(context.Background(), authz.Trusted(project), query, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, it := range page.Items {
		subjects = append(subjects, it.Subject)
	}
	return subjects
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	other := testutil.Project(t, e.pool, "beta")
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	e.add(t, e.pid, d(10), "Alice <alice@example.test>", "bob@example.test", "Welcome aboard", "Your invoice number is 4242")
	e.add(t, e.pid, d(11), "billing@shop.test", "carol@example.test", "Order shipped", "Tracking parcel arrives tomorrow", "X-Run-Id: r1")
	e.add(t, e.pid, d(12), "alice@example.test", "dave@corp.test", "100% off_sale", "Unrelated text")
	e.add(t, other, d(11), "alice@example.test", "bob@example.test", "Welcome aboard", "foreign project secret")

	// A tag rule must exist before ingest for the tag filter.
	if _, err := sqlcgen.New(e.pool).CreateTagRule(context.Background(), sqlcgen.CreateTagRuleParams{
		ProjectID: e.pid, Kind: ingest.RuleHeader, HeaderName: "X-Run-Id", Tag: "run:{value}",
	}); err != nil {
		t.Fatal(err)
	}
	e.add(t, e.pid, d(13), "tagged@example.test", "t@example.test", "Tagged mail", "x", "X-Run-Id: r2")

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"all newest first", "", []string{"Tagged mail", "100% off_sale", "Order shipped", "Welcome aboard"}},
		{"sender substring", "from:alice", []string{"100% off_sale", "Welcome aboard"}},
		{"sender display name", "from:Alice", []string{"100% off_sale", "Welcome aboard"}},
		{"sender domain partial", "from:shop.test", []string{"Order shipped"}},
		{"recipient partial", "to:corp", []string{"100% off_sale"}},
		{"subject partial", "subject:ship", []string{"Order shipped"}},
		{"like metacharacters are literal", "subject:100%", []string{"100% off_sale"}},
		{"underscore is literal", "subject:off_sale", []string{"100% off_sale"}},
		{"percent alone does not match everything", "subject:%", []string{"100% off_sale"}},
		{"body full text", "body:invoice", []string{"Welcome aboard"}},
		{"body does not match subject", "body:welcome", nil},
		{"free text hits body", "parcel", []string{"Order shipped"}},
		{"free text hits subject partially", "aboa", []string{"Welcome aboard"}},
		{"free text phrase", `"parcel arrives"`, []string{"Order shipped"}},
		{"tag exact", "tag:run:r2", []string{"Tagged mail"}},
		{"tag unknown", "tag:nope", nil},
		{"date day", "date:2026-10-11", []string{"Order shipped"}},
		{"after inclusive", "after:2026-10-12", []string{"Tagged mail", "100% off_sale"}},
		{"before exclusive", "before:2026-10-11", []string{"Welcome aboard"}},
		{"combined", "from:alice after:2026-10-11", []string{"100% off_sale"}},
		{"no match", "from:nobody", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := list(t, e, e.pid, tt.query)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}

	t.Run("other project is isolated", func(t *testing.T) {
		if got := list(t, e, other, "body:foreign"); len(got) != 1 {
			t.Errorf("own project: %v", got)
		}
		if got := list(t, e, e.pid, "body:foreign"); len(got) != 0 {
			t.Errorf("leak: %v", got)
		}
	})
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		// Same timestamp for two messages checks the id tiebreaker.
		at := base.Add(time.Duration(i/2) * time.Hour)
		e.add(t, e.pid, at, "a@x.test", "b@x.test", fmt.Sprintf("m%d", i), "body")
	}
	ctx := context.Background()
	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := e.repo.List(ctx, authz.Trusted(e.pid), search.Query{}, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			seen = append(seen, it.Subject)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(seen, ",") != "m4,m3,m2,m1,m0" {
		t.Errorf("paged order: %v", seen)
	}

	if _, err := e.repo.List(ctx, authz.Trusted(e.pid), search.Query{}, "garbage!", 2); !errors.Is(err, messages.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	for _, bad := range []string{"bm90LWEtY3Vyc29y", "YTpi"} { // "not-a-cursor", "a:b"
		if _, err := e.repo.List(ctx, authz.Trusted(e.pid), search.Query{}, bad, 2); !errors.Is(err, messages.ErrInvalidCursor) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if page, _ := e.repo.List(ctx, authz.Trusted(e.pid), search.Query{}, "", 0); len(page.Items) != 5 {
		t.Errorf("default limit: %d", len(page.Items))
	}
	if page, _ := e.repo.List(ctx, authz.Trusted(e.pid), search.Query{}, "", 100000); len(page.Items) != 5 {
		t.Errorf("max limit clamp: %d", len(page.Items))
	}
}

func TestGetAndFriends(t *testing.T) {
	e := newEnv(t)
	other := testutil.Project(t, e.pool, "beta")
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "attachments_inline.eml"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.svc.Ingest(ctx, ingest.Input{ProjectID: e.pid, EnvelopeFrom: "x@x.test", EnvelopeTo: []string{"team@example.test"}, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}

	d, err := e.repo.Get(ctx, authz.Trusted(e.pid), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "Report with files" || !d.HasHTML || len(d.Attachments) != 2 || len(d.Recipients) == 0 {
		t.Errorf("detail: %+v", d)
	}
	if d.Tags == nil || d.Links == nil || d.Headers["Subject"][0] != "Report with files" {
		t.Errorf("collections must be non-nil: %+v", d)
	}
	if d.ReadAt != nil {
		t.Error("new message must be unread")
	}

	got, err := e.repo.Raw(ctx, authz.Trusted(e.pid), id)
	if err != nil || string(got) != string(raw) {
		t.Errorf("raw: %v", err)
	}
	html, err := e.repo.HTML(ctx, authz.Trusted(e.pid), id)
	if err != nil || !strings.Contains(html, "cid:logo123") {
		t.Errorf("html: %q %v", html, err)
	}
	a, content, err := e.repo.AttachmentContent(ctx, authz.Trusted(e.pid), id, 0)
	if err != nil || a.Filename != "report.pdf" || string(content) != "%PDF-1.4 fake pdf bytes" {
		t.Errorf("attachment: %+v %q %v", a, content, err)
	}
	if _, _, err := e.repo.AttachmentContent(ctx, authz.Trusted(e.pid), id, 9); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("missing attachment: %v", err)
	}

	if err := e.repo.MarkRead(ctx, authz.Trusted(e.pid), id); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.repo.Get(ctx, authz.Trusted(e.pid), id); d.ReadAt == nil {
		t.Error("read_at not set")
	}

	// Another project must see nothing at all, for every accessor.
	if _, err := e.repo.Get(ctx, authz.Trusted(other), id); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("get across projects: %v", err)
	}
	if _, err := e.repo.Raw(ctx, authz.Trusted(other), id); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("raw across projects: %v", err)
	}
	if _, err := e.repo.HTML(ctx, authz.Trusted(other), id); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("html across projects: %v", err)
	}
	if _, _, err := e.repo.AttachmentContent(ctx, authz.Trusted(other), id, 0); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("attachment across projects: %v", err)
	}
	if err := e.repo.MarkRead(ctx, authz.Trusted(other), id); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseErrors(t *testing.T) {
	e := newEnv(t)
	e.pool.Close()
	ctx := context.Background()
	if _, err := e.repo.List(ctx, authz.Trusted(1), search.Query{}, "", 1); err == nil {
		t.Error("list")
	}
	if _, err := e.repo.Get(ctx, authz.Trusted(1), 1); err == nil || errors.Is(err, messages.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := e.repo.Raw(ctx, authz.Trusted(1), 1); err == nil || errors.Is(err, messages.ErrNotFound) {
		t.Errorf("raw: %v", err)
	}
	if _, err := e.repo.HTML(ctx, authz.Trusted(1), 1); err == nil || errors.Is(err, messages.ErrNotFound) {
		t.Errorf("html: %v", err)
	}
	if err := e.repo.MarkRead(ctx, authz.Trusted(1), 1); err == nil {
		t.Error("mark read")
	}
}

func TestListScalesWithIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	e := newEnv(t)
	ctx := context.Background()
	// 20k rows in one statement, then make sure the query planner can use the
	// trigram and full text indexes for the filtered paths.
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO messages (project_id, received_at, from_addr, subject, size_bytes, text_body, raw)
		SELECT $1, now() - (g || ' seconds')::interval, 'user' || g || '@example.test',
		       'Subject ' || g, 10, 'body words ' || g, ''::bytea
		FROM generate_series(1, 20000) g`, e.pid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, "ANALYZE messages"); err != nil {
		t.Fatal(err)
	}
	q, _ := search.Parse("from:user19999")
	start := time.Now()
	page, err := e.repo.List(ctx, authz.Trusted(e.pid), q, "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%v %d", err, len(page.Items))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("filtered list too slow: %v", d)
	}
}
