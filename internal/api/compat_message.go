package api

import (
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

const latestID = "latest"

// compatMessageID resolves the message id of a request. "latest" is the
// newest message, optionally narrowed by ?query=. An id that is not a number,
// does not exist or belongs to another project is the same 404.
func (s *server) compatMessageID(w http.ResponseWriter, r *http.Request, c *compatCall, raw string) (int64, bool) {
	if raw == latestID {
		var f search.Filter
		if q := strings.TrimSpace(r.URL.Query().Get("query")); q != "" {
			var err error
			if f, err = compatFilter(r, q); err != nil {
				s.compatFail(w, r, err)
				return 0, false
			}
		}
		id, err := s.Messages.LatestID(r.Context(), c.scope, f)
		if err != nil {
			s.compatFail(w, r, err)
			return 0, false
		}
		return id, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		compatNotFound(w)
		return 0, false
	}
	return id, true
}

// compatMessage is GET /message/{ID}. Like Mailpit it marks the message read.
func (s *server) compatMessage(w http.ResponseWriter, r *http.Request, c *compatCall) {
	id, ok := s.compatMessageID(w, r, c, r.PathValue("ID"))
	if !ok {
		return
	}
	ctx := r.Context()
	d, err := s.Messages.Get(ctx, c.scope, id)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	htmlBody := ""
	if d.HasHTML {
		if htmlBody, err = s.Messages.HTML(ctx, c.scope, id); err != nil {
			s.compatFail(w, r, err)
			return
		}
	}
	var sums map[int]messages.Checksums
	if len(d.Attachments) > 0 {
		if sums, err = s.Messages.AttachmentSums(ctx, c.scope, id); err != nil {
			s.compatFail(w, r, err)
			return
		}
	}
	if err := s.Messages.MarkRead(ctx, c.scope, id); err != nil {
		s.compatFail(w, r, err)
		return
	}
	writeCompatJSON(w, messageOf(d, htmlBody, sums))
}

// compatHeaders is GET /message/{ID}/headers.
func (s *server) compatHeaders(w http.ResponseWriter, r *http.Request, c *compatCall) {
	id, ok := s.compatMessageID(w, r, c, r.PathValue("ID"))
	if !ok {
		return
	}
	d, err := s.Messages.Get(r.Context(), c.scope, id)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	writeCompatJSON(w, d.Headers)
}

// compatRaw is GET /message/{ID}/raw, the source as plain text, or a download
// with ?dl=1.
func (s *server) compatRaw(w http.ResponseWriter, r *http.Request, c *compatCall) {
	id, ok := s.compatMessageID(w, r, c, r.PathValue("ID"))
	if !ok {
		return
	}
	raw, err := s.Messages.Raw(r.Context(), c.scope, id)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", attachmentCSP)
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": idString(id) + ".eml"}))
	}
	_, _ = w.Write(raw)
}

// scriptableTypes are shown as downloads: served inline they could run script
// on the Sandpost origin.
var scriptableTypes = map[string]bool{
	"text/html": true, "text/xml": true, "application/xhtml+xml": true,
	"image/svg+xml": true, "application/xml": true, "text/xsl": true,
}

// compatPart is GET /message/{ID}/part/{PartID}. Parts that a browser would
// execute are served as downloads, the same rule as Mailpit.
func (s *server) compatPart(w http.ResponseWriter, r *http.Request, c *compatCall) {
	id, ok := s.compatMessageID(w, r, c, r.PathValue("ID"))
	if !ok {
		return
	}
	ord, err := strconv.Atoi(r.PathValue("PartID"))
	if err != nil || ord < 0 {
		compatNotFound(w)
		return
	}
	a, content, err := s.Messages.AttachmentContent(r.Context(), c.scope, id, ord)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	name := a.Filename
	if name == "" {
		name = a.ContentID
	}
	ct, disposition := a.ContentType, "inline"
	mediaType, _, _ := strings.Cut(strings.ToLower(ct), ";")
	if scriptableTypes[strings.TrimSpace(mediaType)] {
		ct, disposition = "application/octet-stream", "attachment"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if name != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", attachmentCSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(content)
}

// compatView serves GET /view/{ID}.html and /view/{ID}.txt, the HTML or text
// part of a message for UI tests. Like Mailpit it marks the message read.
func (s *server) compatView(w http.ResponseWriter, r *http.Request, c *compatCall) {
	name := r.PathValue("name")
	var isHTML bool
	switch {
	case strings.HasSuffix(name, ".html"):
		name, isHTML = strings.TrimSuffix(name, ".html"), true
	case strings.HasSuffix(name, ".txt"):
		name = strings.TrimSuffix(name, ".txt")
	default:
		compatNotFound(w)
		return
	}
	id, ok := s.compatMessageID(w, r, c, name)
	if !ok {
		return
	}
	ctx := r.Context()
	d, err := s.Messages.Get(ctx, c.scope, id)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	body, ct := d.Text, "text/plain; charset=utf-8"
	if isHTML {
		if !d.HasHTML {
			compatText(w, http.StatusNotFound, "This message does not contain a HTML part")
			return
		}
		if body, err = s.Messages.HTML(ctx, c.scope, id); err != nil {
			s.compatFail(w, r, err)
			return
		}
		body, ct = linkInlineImages(body, d, c.root+CompatPrefix), "text/html; charset=utf-8"
		w.Header().Set("Content-Security-Policy", htmlCSP)
	} else {
		w.Header().Set("Content-Security-Policy", attachmentCSP)
	}
	if err := s.Messages.MarkRead(ctx, c.scope, id); err != nil {
		s.compatFail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// linkInlineImages points cid: references at the part endpoint of this API.
func linkInlineImages(html string, d messages.Detail, apiBase string) string {
	for _, a := range d.Attachments {
		if a.ContentID == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)cid:` + regexp.QuoteMeta(a.ContentID))
		target := apiBase + "/message/" + idString(d.ID) + "/part/" + strconv.Itoa(a.Ordinal)
		html = re.ReplaceAllLiteralString(html, target)
	}
	return html
}
