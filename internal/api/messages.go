package api

import (
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

// htmlCSP locks the preview down: no scripts, no forms, no network. Images
// load only from this origin (inline cid parts) or data: URIs, so remote
// tracking pixels and trackers are blocked by default.
const htmlCSP = "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; " +
	"img-src 'self' data:; style-src 'unsafe-inline'; font-src data:; base-uri 'none'; form-action 'none'"

// attachmentCSP applies to downloads that a browser might otherwise render.
const attachmentCSP = "sandbox; default-src 'none'"

func (s *server) listMessages(w http.ResponseWriter, r *http.Request, c *call) {
	q, err := search.Parse(r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	page, err := s.Messages.List(r.Context(), c.scope, q, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) getMessage(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	d, err := s.Messages.Get(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) markRead(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	if err := s.Messages.MarkRead(r.Context(), c.scope, mid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rawMessage serves the original bytes as text for the source view, or as an
// .eml download with ?download=1.
func (s *server) rawMessage(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	raw, err := s.Messages.Raw(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", attachmentCSP)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fmt.Sprintf("message-%d.eml", mid)}))
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(raw)
}

// previewURL mints a short lived signed URL for the sandboxed iframe. The
// iframe has an opaque origin, so it can not carry the session cookie.
func (s *server) previewURL(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	d, err := s.Messages.Get(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !d.HasHTML {
		writeError(w, http.StatusNotFound, "message has no HTML part")
		return
	}
	exp, sig := s.Signer.Sign(c.scope, mid, "html", authz.DefaultGrantTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":        signedURL(c.scope.ProjectID(), mid, "html", exp, sig),
		"expires_at": time.Unix(exp, 0).UTC(),
	})
}

// signedURL builds a path that carries a grant for one resource of a message.
func signedURL(pid, mid int64, resource string, exp int64, sig string) string {
	path := "html"
	if rest, ok := strings.CutPrefix(resource, "attachment/"); ok {
		path = "attachments/" + rest
	}
	return fmt.Sprintf("%s/projects/%d/messages/%d/%s?%s=%d.%s", V1Prefix, pid, mid, path, grantParam, exp, sig)
}

// htmlMessage serves the HTML body for the sandboxed iframe, with inline cid:
// references pointing at signed attachment URLs.
func (s *server) htmlMessage(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	html, err := s.Messages.HTML(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if html == "" {
		writeError(w, http.StatusNotFound, "message has no HTML part")
		return
	}
	d, err := s.Messages.Get(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", htmlCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(`<base target="_blank">` + s.rewriteCIDs(html, c.scope, mid, d.Attachments)))
}

// rewriteCIDs points cid: references at attachment URLs carrying a fresh
// signed grant, valid for the same short time as the preview itself.
func (s *server) rewriteCIDs(html string, scope authz.Scope, mid int64, atts []messages.Attachment) string {
	var pairs []string
	for _, a := range atts {
		if a.ContentID == "" {
			continue
		}
		res := "attachment/" + strconv.Itoa(a.Ordinal)
		exp, sig := s.Signer.Sign(scope, mid, res, authz.DefaultGrantTTL)
		pairs = append(pairs, "cid:"+a.ContentID, signedURL(scope.ProjectID(), mid, res, exp, sig))
	}
	if len(pairs) == 0 {
		return html
	}
	return strings.NewReplacer(pairs...).Replace(html)
}

func (s *server) attachment(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	ord, err := strconv.Atoi(r.PathValue("ordinal"))
	if err != nil || ord < 0 {
		writeError(w, http.StatusBadRequest, "invalid ordinal")
		return
	}
	a, content, err := s.Messages.AttachmentContent(r.Context(), c.scope, mid, ord)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := a.Filename
	if name == "" {
		name = fmt.Sprintf("attachment-%d", a.Ordinal)
	}
	ct := a.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", attachmentCSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(content)
}
