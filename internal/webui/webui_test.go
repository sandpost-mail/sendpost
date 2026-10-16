package webui

import (
	"net/http"
	"net/http/httptest"
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
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
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

func TestEmbeddedHandlerBuilds(t *testing.T) {
	if Handler() == nil {
		t.Fatal("nil handler")
	}
}
