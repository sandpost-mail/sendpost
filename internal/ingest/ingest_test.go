package ingest_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngestStoresEverything(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	q := sqlcgen.New(pool)
	for _, r := range []sqlcgen.CreateTagRuleParams{
		{ProjectID: pid, Kind: ingest.RuleHeader, HeaderName: "X-Run-Id", Tag: "run:{value}"},
		{ProjectID: pid, Kind: ingest.RuleRecipient, Pattern: "*@example.test", Tag: "internal"},
	} {
		if _, err := q.CreateTagRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	svc := ingest.NewService(pool)
	raw := fixture(t, "simple_text.eml")

	id, err := svc.Ingest(ctx, ingest.Input{
		ProjectID: pid, EnvelopeFrom: "bounce@example.test", EnvelopeTo: []string{"bob@example.test"}, Raw: raw,
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := q.GetMessage(ctx, sqlcgen.GetMessageParams{ProjectID: pid, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Plain hello" || m.SizeBytes != int64(len(raw)) || !strings.Contains(m.TextBody, "Hello Bob") {
		t.Errorf("message: %+v", m)
	}
	gotRaw, err := q.GetMessageRaw(ctx, sqlcgen.GetMessageRawParams{ProjectID: pid, ID: id})
	if err != nil || string(gotRaw) != string(raw) {
		t.Errorf("raw not stored byte for byte: %v", err)
	}
	rc, err := q.ListMessageRecipients(ctx, id)
	if err != nil || len(rc) != 4 { // to bob, to carol, cc dave, envelope bob
		t.Errorf("recipients: %+v %v", rc, err)
	}
	links, _ := q.ListMessageLinks(ctx, id)
	if len(links) != 2 {
		t.Errorf("links: %v", links)
	}
	tags, _ := q.ListMessageTagNames(ctx, id)
	if strings.Join(tags, ",") != "internal,run:run-42" {
		t.Errorf("tags: %v", tags)
	}
}

func TestIngestAttachmentsAndNotify(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	pid := testutil.Project(t, pool, "alpha")
	other := testutil.Project(t, pool, "beta")

	bus := events.NewBus(dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.Run(bctx) }()
	t.Cleanup(func() { cancel(); <-done })
	sub, otherSub := bus.Subscribe(pid), bus.Subscribe(other)
	<-sub.Ready()
	<-otherSub.Ready()

	svc := ingest.NewService(pool)
	id, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, Raw: fixture(t, "attachments_inline.eml")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != events.TypeMessageCreated || ev.MessageID != id {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
	select {
	case ev := <-otherSub.C:
		t.Fatalf("event leaked to another project: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}

	atts, err := sqlcgen.New(pool).ListMessageAttachments(ctx, id)
	if err != nil || len(atts) != 2 {
		t.Fatalf("attachments: %+v %v", atts, err)
	}
	if atts[0].Filename != "report.pdf" || atts[1].ContentID != "logo123" || !atts[1].Inline {
		t.Errorf("attachments: %+v", atts)
	}
}

func TestIngestAlwaysCaptures(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	svc := ingest.NewService(pool)
	for _, name := range []string{"malformed_no_headers.eml", "multipart_missing_boundary.eml", "nul_and_invalid.eml", "html_only.eml", "charset_latin1.eml"} {
		if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, EnvelopeFrom: "a@x.test", EnvelopeTo: []string{"b@x.test"}, Raw: fixture(t, name)}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	huge := []byte("From: a@x.test\r\nSubject: big\r\n\r\n" + strings.Repeat("word ", 400_000))
	if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, Raw: huge}); err != nil {
		t.Errorf("huge body must not break the tsvector: %v", err)
	}
	if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, Raw: nil}); err != nil {
		t.Errorf("empty message: %v", err)
	}
}

func TestIngestErrors(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	svc := ingest.NewService(pool)
	if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: 12345, Raw: []byte("Subject: x\r\n\r\nb")}); !errors.Is(err, ingest.ErrProjectNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	pool.Close()
	if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: 1, Raw: []byte("x")}); err == nil {
		t.Error("closed pool must fail")
	}
}

func TestIngestIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	// A tag longer than the column check allows, via a rule row bypassing
	// validation, makes the tag step fail after the message insert.
	if _, err := pool.Exec(ctx, `ALTER TABLE tags DROP CONSTRAINT tags_name_check;
		ALTER TABLE tags ADD CONSTRAINT tags_name_check CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlcgen.New(pool).CreateTagRule(ctx, sqlcgen.CreateTagRuleParams{
		ProjectID: pid, Kind: ingest.RuleRecipient, Pattern: "*", Tag: "x",
	}); err != nil {
		t.Fatal(err)
	}
	svc := ingest.NewService(pool)
	_, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, EnvelopeTo: []string{"a@x.test"}, Raw: []byte("Subject: x\r\n\r\nb")})
	if err == nil {
		t.Fatal("expected failure")
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil || n != 0 {
		t.Errorf("partial message left behind: %d %v", n, err)
	}
}
