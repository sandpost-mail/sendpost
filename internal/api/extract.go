package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/extract"
)

// extractResponse is what a CI script needs out of one message: the links in
// order of appearance (anchors first) and the one-time codes found.
type extractResponse struct {
	MessageID int64          `json:"message_id"`
	Links     []string       `json:"links"`
	Codes     []extract.Code `json:"codes"`
}

// extractMessage returns the links and one-time codes of a message. Optional
// filters: link (case insensitive substring a link must contain) and
// code_pattern (RE2 regular expression, replaces the code heuristics).
func (s *server) extractMessage(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	d, err := s.Messages.Get(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	codes, err := extract.Codes(d.Text, r.URL.Query().Get("code_pattern"))
	if errors.Is(err, extract.ErrBadPattern) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	links := d.Links
	if f := strings.ToLower(r.URL.Query().Get("link")); f != "" {
		links = make([]string, 0, len(d.Links))
		for _, l := range d.Links {
			if strings.Contains(strings.ToLower(l), f) {
				links = append(links, l)
			}
		}
	}
	writeJSON(w, http.StatusOK, extractResponse{MessageID: d.ID, Links: links, Codes: codes})
}
