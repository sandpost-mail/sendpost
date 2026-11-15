package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
)

func TestRewriteCIDsMatchesWholeIDs(t *testing.T) {
	atts := []messages.Attachment{
		{Ordinal: 0, ContentID: "img1"},
		{Ordinal: 9, ContentID: "img10"},
		{Ordinal: 3, ContentID: "Logo@Example"},
		{Ordinal: 4},
	}
	urlFor := func(a messages.Attachment) string { return "/part/" + strconv.Itoa(a.Ordinal) }
	cases := []struct{ name, in, want string }{
		{"prefix id", `<img src="cid:img10">`, `<img src="/part/9">`},
		{"short id", `<img src="cid:img1">`, `<img src="/part/0">`},
		{"case insensitive", `<img src='CID:logo@example'>`, `<img src='/part/3'>`},
		{"css url", `background:url(cid:img1)`, `background:url(/part/0)`},
		{"unknown id kept", `<img src="cid:other">`, `<img src="cid:other">`},
		{"both", `cid:img1 cid:img10`, `/part/0 /part/9`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteCIDs(tc.in, atts, urlFor); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestWithBaseKeepsTheDoctypeFirst(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<!DOCTYPE html><html>x</html>", `<!DOCTYPE html><base target="_blank"><html>x</html>`},
		{"\n <!doctype html PUBLIC \"x\">\n<p>", "\n <!doctype html PUBLIC \"x\"><base target=\"_blank\">\n<p>"},
		{"<p>hi</p>", `<base target="_blank"><p>hi</p>`},
	}
	for _, tc := range cases {
		if got := withBase(tc.in); got != tc.want {
			t.Errorf("withBase(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestServePartPolicy(t *testing.T) {
	cases := []struct {
		ct, wantCT, wantDisp string
		inline               bool
	}{
		{"image/png", "image/png", "inline", true},
		{"IMAGE/JPEG; name=a.jpg", "IMAGE/JPEG; name=a.jpg", "inline", true},
		{"text/plain; charset=utf-8", "text/plain; charset=utf-8", "inline", true},
		{"application/pdf", "application/pdf", "inline", true},
		{"image/png", "image/png", "attachment", false},
		{"image/svg+xml", "application/octet-stream", "attachment", true},
		{"text/html", "application/octet-stream", "attachment", true},
		{"application/rss+xml", "application/octet-stream", "attachment", true},
		{"application/mathml+xml", "application/octet-stream", "attachment", true},
		{"", "application/octet-stream", "attachment", true},
		{"not a type", "application/octet-stream", "attachment", true},
	}
	for _, tc := range cases {
		t.Run(tc.ct, func(t *testing.T) {
			rec := httptest.NewRecorder()
			servePart(rec, tc.ct, "f.bin", tc.inline, []byte("x"))
			h := rec.Header()
			if h.Get("Content-Type") != tc.wantCT {
				t.Errorf("content type %q want %q", h.Get("Content-Type"), tc.wantCT)
			}
			if !strings.HasPrefix(h.Get("Content-Disposition"), tc.wantDisp+";") {
				t.Errorf("disposition %q want %s", h.Get("Content-Disposition"), tc.wantDisp)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != attachmentCSP {
				t.Errorf("headers %v", h)
			}
		})
	}
}

func TestDecodeBody(t *testing.T) {
	type in struct {
		Label string `json:"label"`
	}
	cases := []struct {
		name, body string
		status     int
	}{
		{"one value", `{"label":"a"}`, 0},
		{"trailing whitespace", "{\"label\":\"a\"}\n", 0},
		{"empty", ``, 0},
		{"two values", `{"label":"a"}{"label":"b"}`, http.StatusBadRequest},
		{"garbage after", `{"label":"a"} garbage`, http.StatusBadRequest},
		{"unknown field", `{"x":1}`, http.StatusBadRequest},
		{"too large", `{"label":"` + strings.Repeat("a", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
			var v in
			ok := decodeBody(rec, r, &v)
			if tc.status == 0 {
				if !ok {
					t.Fatalf("rejected: %d %s", rec.Code, rec.Body)
				}
				return
			}
			if ok || rec.Code != tc.status {
				t.Errorf("ok %v status %d want %d", ok, rec.Code, tc.status)
			}
		})
	}
}

func TestStreamLimits(t *testing.T) {
	l := newStreamLimits(2)
	if !l.acquire("a") {
		t.Fatal("first refused")
	}
	if !l.acquire("a") {
		t.Fatal("second refused")
	}
	if l.acquire("a") {
		t.Fatal("third accepted")
	}
	if !l.acquire("b") {
		t.Fatal("other key refused")
	}
	l.release("a")
	if !l.acquire("a") {
		t.Fatal("slot not released")
	}
	keys := map[string]bool{
		streamKey(authz.Principal{TokenID: 7, UserID: 1}):           true,
		streamKey(authz.Principal{SessionID: []byte{1}, UserID: 1}): true,
		streamKey(authz.Principal{SessionID: []byte{2}, UserID: 1}): true,
	}
	if len(keys) != 3 {
		t.Errorf("credentials share a key: %v", keys)
	}
}

func TestAccessLogRecordsAndRecoversPanics(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom/{id}", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	mux.HandleFunc("GET /ok/{id}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) })
	h := accessLog(log, mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ok/1?token=secret", nil))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom/2?grant=secret", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panic answered %d", rec.Code)
	}
	out := buf.String()
	for _, want := range []string{`"pattern":"GET /ok/{id}"`, `"status":200`, `"bytes":5`, `"handler panic"`, `"stack":`, `"status":500`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "/ok/1") {
		t.Errorf("log carries the raw path or query:\n%s", out)
	}
}
