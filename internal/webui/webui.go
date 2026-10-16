// Package webui serves the embedded single page app. The SvelteKit build is
// copied into dist/ by `make web`; a fresh checkout only has a placeholder, in
// which case the handler explains how to build the UI.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

const (
	indexFile = "index.html"
	// Scripts and styles come from this origin. SvelteKit's static fallback
	// inlines a small bootstrap script, hence 'unsafe-inline' for scripts.
	shellCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'self'; form-action 'self'"
	immutablePrefix = "_app/immutable/"
	notBuiltMessage = "The Sandpost UI is not built into this binary.\n" +
		"Run `make web` and rebuild, or use the Docker image. The API is available under /_/api/v1.\n"
)

// Handler serves the UI embedded in the binary.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is a compile time embed, this can not fail
	}
	return NewHandler(sub)
}

// NewHandler serves the SPA found in fsys: existing files as they are, every
// other extension-less path as index.html so client side routes work.
func NewHandler(fsys fs.FS) http.Handler {
	return &handler{fsys: fsys}
}

type handler struct{ fsys fs.FS }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexFile
	}
	if data, ok := h.read(name); ok && name != indexFile {
		h.serve(w, r, name, data)
		return
	}
	// A missing file with an extension is a real 404, not a client route.
	if path.Ext(name) != "" && name != indexFile {
		http.NotFound(w, r)
		return
	}
	data, ok := h.read(indexFile)
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notBuiltMessage))
		return
	}
	h.serve(w, r, indexFile, data)
}

func (h *handler) read(name string) ([]byte, bool) {
	info, err := fs.Stat(h.fsys, name)
	if err != nil || info.IsDir() {
		return nil, false
	}
	data, err := fs.ReadFile(h.fsys, name)
	return data, err == nil
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	switch {
	case strings.HasPrefix(name, immutablePrefix):
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		hdr.Set("Cache-Control", "no-cache")
	}
	if path.Ext(name) == ".html" {
		hdr.Set("Content-Security-Policy", shellCSP)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
