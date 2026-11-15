// Package webui serves the embedded single page app. The SvelteKit build is
// copied into dist/ by `make web`; a fresh checkout only has a placeholder, in
// which case the handler explains how to build the UI.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"golang.org/x/net/html"
)

//go:embed all:dist
var embedded embed.FS

const (
	indexFile = "index.html"
	// cspTemplate is the shell's policy. Scripts come from this origin plus
	// the inline bootstrap script SvelteKit writes into index.html, allowed by
	// its hash (the build also carries it in a <meta> CSP, see
	// web/svelte.config.js): no 'unsafe-inline', so javascript: URLs never run.
	cspScriptPlaceholder = "{script-src}"
	cspTemplate          = "default-src 'self'; script-src 'self'" + cspScriptPlaceholder + "; style-src 'self' 'unsafe-inline'; " +
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

// file is a servable file, read once when the handler is built.
type file struct {
	data []byte
	etag string
}

// NewHandler serves the SPA found in fsys: existing files as they are, every
// other extension-less path as index.html so client side routes work. Files
// are read and hashed once, here; requests share the bytes and get an ETag.
func NewHandler(fsys fs.FS) http.Handler {
	files := map[string]file{}
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is served as missing
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil //nolint:nilerr // same
		}
		sum := sha256.Sum256(data)
		files[name] = file{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
		return nil
	})
	csp := strings.Replace(cspTemplate, cspScriptPlaceholder, "", 1)
	if index, ok := files[indexFile]; ok {
		csp = strings.Replace(cspTemplate, cspScriptPlaceholder, inlineScriptHashes(index.data), 1)
	}
	return &handler{files: files, csp: csp}
}

type handler struct {
	files map[string]file
	csp   string
}

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
	if f, ok := h.files[name]; ok && name != indexFile {
		h.serve(w, r, name, f)
		return
	}
	// A missing file with an extension is a real 404, not a client route.
	if path.Ext(name) != "" && name != indexFile {
		http.NotFound(w, r)
		return
	}
	f, ok := h.files[indexFile]
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notBuiltMessage))
		return
	}
	h.serve(w, r, indexFile, f)
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, name string, f file) {
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	// Hashed assets never change; everything else is revalidated with the
	// ETag, which ServeContent turns into a 304 on If-None-Match.
	if strings.HasPrefix(name, immutablePrefix) {
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hdr.Set("Cache-Control", "no-cache")
	}
	hdr.Set("ETag", f.etag)
	if path.Ext(name) == ".html" {
		hdr.Set("Content-Security-Policy", h.csp)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

// inlineScriptHashes returns " 'sha256-...'" for every inline <script> of an
// HTML document, the CSP source that allows exactly that script.
func inlineScriptHashes(doc []byte) string {
	var b strings.Builder
	z := html.NewTokenizer(bytes.NewReader(doc))
	for {
		switch z.Next() {
		case html.ErrorToken: // io.EOF or a malformed tail: done either way
			return b.String()
		case html.StartTagToken:
			tn, hasAttr := z.TagName()
			// The token after an inline <script> is its raw, unescaped text;
			// an empty script runs nothing and needs no hash.
			if string(tn) == "script" && !hasSrc(z, hasAttr) && z.Next() == html.TextToken {
				sum := sha256.Sum256(z.Raw())
				b.WriteString(" 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'")
			}
		}
	}
}

func hasSrc(z *html.Tokenizer, more bool) bool {
	for more {
		var key []byte
		key, _, more = z.TagAttr()
		if string(key) == "src" {
			return true
		}
	}
	return false
}
