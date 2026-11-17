package api

import (
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/messages"
)

// Untrusted mail content is served by both API surfaces with the rules below,
// so a fix to one applies to the other.

// cidRef matches a cid: reference up to the next delimiter of an HTML or CSS
// url, so the whole Content-ID is compared (cid:img1 never matches inside
// cid:img10).
var cidRef = regexp.MustCompile(`(?i)cid:([^"'\s)>]+)`)

// rewriteCIDs replaces cid: references that name an attachment by urlFor of
// that attachment. Content-IDs compare case-insensitively, references to
// unknown ids are left alone.
func rewriteCIDs(html string, atts []messages.Attachment, urlFor func(messages.Attachment) string) string {
	byID := make(map[string]messages.Attachment, len(atts))
	for _, a := range atts {
		if a.ContentID != "" {
			byID[strings.ToLower(a.ContentID)] = a
		}
	}
	if len(byID) == 0 {
		return html
	}
	return cidRef.ReplaceAllStringFunc(html, func(ref string) string {
		a, ok := byID[strings.ToLower(ref[len("cid:"):])]
		if !ok {
			return ref
		}
		return urlFor(a)
	})
}

// doctype matches a leading <!DOCTYPE ...> declaration after optional
// whitespace.
var doctype = regexp.MustCompile(`(?is)^\s*<!doctype[^>]*>`)

// withBase inserts <base target="_blank"> after the doctype, so a standards
// mode mail stays in standards mode (content before the doctype switches the
// browser to quirks mode).
func withBase(html string) string {
	const base = `<base target="_blank">`
	if loc := doctype.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + base + html[loc[1]:]
	}
	return base + html
}

// inlineTypes may be shown in the browser. Everything else is sent as an
// opaque download, an allowlist so that a type nobody thought of (svg, xml
// dialects, html) can never run script on the Sandpost origin.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/avif": true, "image/bmp": true, "text/plain": true, "application/pdf": true,
}

// inlineSafe reports whether a sender supplied content type may be served as is.
func inlineSafe(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && inlineTypes[mt]
}

// servePart writes a message part. Safe types keep their content type and use
// the requested disposition, anything else becomes application/octet-stream
// with an attachment disposition. Every part is sandboxed and nosniff.
func servePart(w http.ResponseWriter, contentType, filename string, inline bool, content []byte) {
	disposition := "attachment"
	if !inlineSafe(contentType) {
		contentType = "application/octet-stream"
	} else if inline {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	params := map[string]string{}
	if filename != "" {
		params["filename"] = filename
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, params))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", attachmentCSP)
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(content) //nolint:gosec // G705: untrusted part served with nosniff and the sandbox attachmentCSP
}

// serveHTML writes a mail's HTML body for a sandboxed view.
func serveHTML(w http.ResponseWriter, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", htmlCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(body)) //nolint:gosec // G705: untrusted HTML is the point; nosniff and the sandbox htmlCSP isolate it
}
