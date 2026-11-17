package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func newFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                  {Data: []byte("<html>shell</html>")},
		"_app/immutable/entry/app.js": {Data: []byte("console.log(1)")},
		"favicon.svg":                 {Data: []byte("<svg/>")},
		"dir/inner.txt":               {Data: []byte("x")},
	}
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))
	return rec
}

func TestServesAssetsAndFallsBackToIndex(t *testing.T) {
	h := NewHandler(newFS())
	tests := []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{"/", 200, "shell"},
		{"/index.html", 200, "shell"},
		{"/projects/3/messages/9", 200, "shell"},
		{"/dir", 200, "shell"}, // directories are client routes
		{"/favicon.svg", 200, "<svg/>"},
		{"/_app/immutable/entry/app.js", 200, "console.log"},
		{"/_app/immutable/missing.js", 404, ""},
		{"/missing.png", 404, ""},
		{"/../../etc/passwd", 200, "shell"}, // cleaned, treated as a route
	}
	for _, tt := range tests {
		rec := get(h, http.MethodGet, tt.path)
		if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
			t.Errorf("%s: %d %q", tt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestHeaders(t *testing.T) {
	h := NewHandler(newFS())
	shell := get(h, http.MethodGet, "/")
	if !strings.Contains(shell.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("shell must forbid framing")
	}
	if shell.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("shell cache: %q", shell.Header().Get("Cache-Control"))
	}
	asset := get(h, http.MethodGet, "/_app/immutable/entry/app.js")
	if !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache: %q", asset.Header().Get("Cache-Control"))
	}
	if asset.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

func TestMethods(t *testing.T) {
	h := NewHandler(newFS())
	if rec := get(h, http.MethodHead, "/"); rec.Code != 200 {
		t.Errorf("HEAD: %d", rec.Code)
	}
	rec := get(h, http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestNotBuilt(t *testing.T) {
	h := NewHandler(fstest.MapFS{".gitkeep": {}})
	rec := get(h, http.MethodGet, "/anything")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestShellCSPHashesInlineScriptsLikeSvelteKit(t *testing.T) {
	// testdata/sveltekit-index.html is a real `npm run build` output with
	// kit.csp mode 'hash': the header must allow exactly the script the
	// <meta> CSP allows, and never 'unsafe-inline' for scripts.
	doc, err := os.ReadFile("testdata/sveltekit-index.html")
	if err != nil {
		t.Fatal(err)
	}
	meta := regexp.MustCompile(`'sha256-[^']+'`).FindString(string(doc))
	if meta == "" {
		t.Fatal("fixture has no meta CSP hash")
	}
	h := NewHandler(fstest.MapFS{"index.html": {Data: doc}})
	csp := get(h, http.MethodGet, "/projects/1").Header().Get("Content-Security-Policy")
	scriptSrc := ""
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src ") {
			scriptSrc = d
		}
	}
	if scriptSrc != "script-src 'self' "+meta {
		t.Fatalf("script-src %q, want the SvelteKit hash %s and no unsafe-inline", scriptSrc, meta)
	}
}

func TestInlineScriptHashesSkipsExternalAndEmptyScripts(t *testing.T) {
	doc := `<script src="/a.js"></script><script></script><script>x()</script>`
	if got := inlineScriptHashes([]byte(doc)); strings.Count(got, "'sha256-") != 1 {
		t.Fatalf("hashes %q, want exactly one", got)
	}
}

func TestETagRevalidation(t *testing.T) {
	h := NewHandler(newFS())
	for _, p := range []string{"/", "/favicon.svg", "/_app/immutable/entry/app.js"} {
		etag := get(h, http.MethodGet, p).Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", p)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("%s: revalidation got %d with %d bytes, want 304", p, rec.Code, rec.Body.Len())
		}
	}
	if get(h, http.MethodGet, "/").Header().Get("ETag") == get(h, http.MethodGet, "/favicon.svg").Header().Get("ETag") {
		t.Error("different files share an ETag")
	}
}

func TestEmbeddedHandlerBuilds(t *testing.T) {
	if Handler() == nil {
		t.Fatal("nil handler")
	}
}
