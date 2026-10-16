package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

type app struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  *httptest.Server
	ing  *ingest.Service
}

func newApp(t *testing.T) *app {
	t.Helper()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	if _, err := store.MigrateUp(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus(dsn, log)
	bctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.Run(bctx) }()
	t.Cleanup(func() { cancel(); <-done })

	h := api.NewHandler(api.Deps{
		DB: pool, Projects: projects.New(pool),
		Credentials: credentials.New(pool, "0123456789abcdef0123456789abcdef"),
		Messages:    messages.New(pool), Bus: bus, Log: log,
		Info: api.ServerInfo{SMTPPort: 1025, SMTPTLSMode: "off", Version: "test"},
		UI:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ui")) }),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &app{t: t, pool: pool, srv: srv, ing: ingest.NewService(pool)}
}

func (a *app) do(method, path string, body any) (*http.Response, []byte) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (a *app) mustJSON(method, path string, body any, wantStatus int, out any) {
	a.t.Helper()
	resp, data := a.do(method, path, body)
	if resp.StatusCode != wantStatus {
		a.t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

func (a *app) project(name string) projects.Project {
	var p projects.Project
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": name}, http.StatusCreated, &p)
	return p
}

func (a *app) capture(pid int64, fixtureName string) int64 {
	a.t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", fixtureName)) //nolint:gosec // test fixture path
	if err != nil {
		a.t.Fatal(err)
	}
	id, err := a.ing.Ingest(context.Background(), ingest.Input{ProjectID: pid, EnvelopeTo: []string{"x@example.test"}, Raw: raw})
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

func pp(pid int64, format string, args ...any) string {
	return fmt.Sprintf("%s/projects/%d", api.V1Prefix, pid) + fmt.Sprintf(format, args...)
}

func TestProjectsAndInfo(t *testing.T) {
	a := newApp(t)
	var info api.ServerInfo
	a.mustJSON(http.MethodGet, api.V1Prefix+"/info", nil, http.StatusOK, &info)
	if info.SMTPPort != 1025 || info.Version != "test" {
		t.Errorf("info: %+v", info)
	}

	p := a.project("Billing API")
	if p.Slug != "billing-api" {
		t.Errorf("slug %q", p.Slug)
	}
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": "x", "slug": "billing-api"}, http.StatusConflict, nil)
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": ""}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]any{"name": "x", "bogus": 1}, http.StatusBadRequest, nil)

	var list struct{ Items []projects.Project }
	a.mustJSON(http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 1 {
		t.Errorf("list: %+v", list)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, ""), nil, http.StatusOK, nil)
	a.mustJSON(http.MethodGet, pp(9999, ""), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodGet, api.V1Prefix+"/projects/abc", nil, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodGet, api.V1Prefix+"/projects/-1", nil, http.StatusBadRequest, nil)
}

func TestWritesRequireJSON(t *testing.T) {
	a := newApp(t)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.srv.URL+api.V1Prefix+"/projects", strings.NewReader(`{"name":"x"}`))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("%q: status %d", ct, resp.StatusCode)
		}
	}
	// charset parameters are fine
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.srv.URL+api.V1Prefix+"/projects", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("json with charset: %d", resp.StatusCode)
	}
	// Malformed JSON.
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, a.srv.URL+api.V1Prefix+"/projects", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad json: %d", resp.StatusCode)
	}
}

func TestCredentialsEndpoints(t *testing.T) {
	a := newApp(t)
	p, other := a.project("A"), a.project("B")

	var issued credentials.Issued
	a.mustJSON(http.MethodPost, pp(p.ID, "/credentials"), map[string]string{"label": "ci"}, http.StatusCreated, &issued)
	if issued.Secret == "" || issued.Label != "ci" {
		t.Fatalf("issued: %+v", issued)
	}
	a.mustJSON(http.MethodPost, pp(p.ID, "/credentials"), map[string]string{"label": strings.Repeat("x", 200)}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodPost, pp(9999, "/credentials"), map[string]string{}, http.StatusNotFound, nil)

	resp, body := a.do(http.MethodGet, pp(p.ID, "/credentials"), nil)
	if resp.StatusCode != 200 || strings.Contains(string(body), issued.Secret) || strings.Contains(string(body), "secret") {
		t.Errorf("list must never contain secrets: %s", body)
	}
	var list struct{ Items []credentials.Credential }
	_ = json.Unmarshal(body, &list)
	if len(list.Items) != 1 || list.Items[0].Username != issued.Username {
		t.Errorf("list: %s", body)
	}

	a.mustJSON(http.MethodDelete, pp(other.ID, "/credentials/%d", issued.ID), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/credentials/%d", issued.ID), nil, http.StatusNoContent, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/credentials/%d", issued.ID), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/credentials/x"), nil, http.StatusBadRequest, nil)
}

func TestTagRuleEndpoints(t *testing.T) {
	a := newApp(t)
	p := a.project("A")
	var rule projects.TagRule
	a.mustJSON(http.MethodPost, pp(p.ID, "/tag-rules"),
		map[string]string{"kind": "header", "header_name": "X-Run-Id", "tag": "run:{value}"}, http.StatusCreated, &rule)
	a.mustJSON(http.MethodPost, pp(p.ID, "/tag-rules"), map[string]string{"kind": "nope", "tag": "t"}, http.StatusBadRequest, nil)
	var list struct{ Items []projects.TagRule }
	a.mustJSON(http.MethodGet, pp(p.ID, "/tag-rules"), nil, http.StatusOK, &list)
	if len(list.Items) != 1 {
		t.Errorf("rules: %+v", list)
	}

	// The rule is applied to the next captured message.
	raw := "From: a@x.test\r\nTo: b@x.test\r\nSubject: s\r\nX-Run-Id: 77\r\n\r\nb"
	if _, err := a.ing.Ingest(context.Background(), ingest.Input{ProjectID: p.ID, Raw: []byte(raw)}); err != nil {
		t.Fatal(err)
	}
	var page messages.Page
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?q=tag:run:77"), nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].Tags[0] != "run:77" {
		t.Errorf("tagged message: %+v", page)
	}

	a.mustJSON(http.MethodDelete, pp(p.ID, "/tag-rules/%d", rule.ID), nil, http.StatusNoContent, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/tag-rules/%d", rule.ID), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/tag-rules/z"), nil, http.StatusBadRequest, nil)
}

func TestMessageEndpoints(t *testing.T) {
	a := newApp(t)
	p := a.project("A")
	mid := a.capture(p.ID, "attachments_inline.eml")
	a.capture(p.ID, "simple_text.eml")

	var page messages.Page
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?limit=1"), nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page: %+v", page)
	}
	var page2 messages.Page
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?limit=1&cursor=%s", page.NextCursor), nil, http.StatusOK, &page2)
	if len(page2.Items) != 1 || page2.Items[0].ID != mid || page2.NextCursor != "" {
		t.Errorf("page2: %+v", page2)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?q=subject:files"), nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != mid {
		t.Errorf("search: %+v", page)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?q=date:nope"), nil, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?limit=abc"), nil, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages?cursor=zzz!"), nil, http.StatusBadRequest, nil)

	var d messages.Detail
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d", mid), nil, http.StatusOK, &d)
	if d.Subject != "Report with files" || len(d.Attachments) != 2 || !d.HasHTML {
		t.Errorf("detail: %+v", d)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/999999"), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/x"), nil, http.StatusBadRequest, nil)

	a.mustJSON(http.MethodPost, pp(p.ID, "/messages/%d/read", mid), map[string]string{}, http.StatusNoContent, nil)
	a.mustJSON(http.MethodPost, pp(p.ID, "/messages/x/read"), map[string]string{}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d", mid), nil, http.StatusOK, &d)
	if d.ReadAt == nil {
		t.Error("read_at not set")
	}

	t.Run("raw", func(t *testing.T) {
		resp, body := a.do(http.MethodGet, pp(p.ID, "/messages/%d/raw", mid), nil)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.Contains(string(body), "Subject: Report with files") {
			t.Errorf("raw: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		resp, _ = a.do(http.MethodGet, pp(p.ID, "/messages/%d/raw?download=1", mid), nil)
		if resp.Header.Get("Content-Type") != "message/rfc822" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
			t.Errorf("download headers: %v", resp.Header)
		}
	})

	t.Run("html is sandboxed and cid rewritten", func(t *testing.T) {
		resp, body := a.do(http.MethodGet, pp(p.ID, "/messages/%d/html", mid), nil)
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"sandbox", "default-src 'none'", "img-src 'self' data:"} {
			if !strings.Contains(csp, want) {
				t.Errorf("CSP %q missing %q", csp, want)
			}
		}
		if strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
			t.Errorf("CSP too permissive: %s", csp)
		}
		html := string(body)
		if strings.Contains(html, "cid:logo123") || !strings.Contains(html, "/attachments/1") {
			t.Errorf("cid not rewritten: %s", html)
		}
		if !strings.HasPrefix(html, `<base target="_blank">`) {
			t.Error("links must open outside the preview")
		}
	})

	t.Run("html missing", func(_ *testing.T) {
		textOnly := a.capture(p.ID, "simple_text.eml")
		a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/html", textOnly), nil, http.StatusNotFound, nil)
	})

	t.Run("attachment", func(t *testing.T) {
		resp, body := a.do(http.MethodGet, pp(p.ID, "/messages/%d/attachments/0", mid), nil)
		if resp.StatusCode != 200 || string(body) != "%PDF-1.4 fake pdf bytes" {
			t.Fatalf("attachment: %d %q", resp.StatusCode, body)
		}
		if !strings.Contains(resp.Header.Get("Content-Disposition"), `filename=report.pdf`) ||
			resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers: %v", resp.Header)
		}
		a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/attachments/7", mid), nil, http.StatusNotFound, nil)
		a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/attachments/-1", mid), nil, http.StatusBadRequest, nil)
		a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/attachments/x", mid), nil, http.StatusBadRequest, nil)
	})
}

// TestCrossProjectIsolation requests every message endpoint with another
// project's message id and expects nothing to leak.
func TestCrossProjectIsolation(t *testing.T) {
	a := newApp(t)
	owner, intruder := a.project("Owner"), a.project("Intruder")
	mid := a.capture(owner.ID, "attachments_inline.eml")

	for _, path := range []string{
		"/messages/%d", "/messages/%d/raw", "/messages/%d/html", "/messages/%d/attachments/0",
	} {
		resp, body := a.do(http.MethodGet, pp(intruder.ID, path, mid), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d %s", path, resp.StatusCode, body)
		}
	}
	resp, _ := a.do(http.MethodPost, pp(intruder.ID, "/messages/%d/read", mid), map[string]string{})
	if resp.StatusCode != http.StatusNoContent { // no-op by construction
		t.Errorf("read: %d", resp.StatusCode)
	}
	var d messages.Detail
	a.mustJSON(http.MethodGet, pp(owner.ID, "/messages/%d", mid), nil, http.StatusOK, &d)
	if d.ReadAt != nil {
		t.Error("another project marked the message as read")
	}
	var page messages.Page
	a.mustJSON(http.MethodGet, pp(intruder.ID, "/messages?q=report"), nil, http.StatusOK, &page)
	if len(page.Items) != 0 {
		t.Errorf("list leaked: %+v", page)
	}
}

func TestServerErrorsDoNotLeakDetails(t *testing.T) {
	a := newApp(t)
	a.pool.Close()
	resp, body := a.do(http.MethodGet, api.V1Prefix+"/projects", nil)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "internal error") ||
		strings.Contains(string(body), "closed") || strings.Contains(string(body), "pool") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}

func TestUIFallbackAndHealth(t *testing.T) {
	a := newApp(t)
	resp, body := a.do(http.MethodGet, "/some/spa/route", nil)
	if resp.StatusCode != 200 || string(body) != "ui" {
		t.Errorf("ui fallback: %d %q", resp.StatusCode, body)
	}
	resp, _ = a.do(http.MethodGet, "/readyz", nil)
	if resp.StatusCode != 200 {
		t.Errorf("readyz: %d", resp.StatusCode)
	}
	// Unknown API paths answer a JSON 404, never the SPA shell.
	resp, body = a.do(http.MethodGet, api.V1Prefix+"/nope", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"error"`) {
		t.Errorf("unknown api path: %d %q", resp.StatusCode, body)
	}
}

func TestEventStream(t *testing.T) {
	a := newApp(t)
	p := a.project("A")
	other := a.project("B")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.srv.URL+pp(p.ID, "/events"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	expect := func(substr string) {
		t.Helper()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed waiting for %q", substr)
				}
				if strings.Contains(l, substr) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q", substr)
			}
		}
	}
	expect("event: ready")

	a.capture(other.ID, "simple_text.eml") // must not appear
	id := a.capture(p.ID, "simple_text.eml")
	expect("event: message.created")
	expect(fmt.Sprintf(`"message_id":%d`, id))
}
