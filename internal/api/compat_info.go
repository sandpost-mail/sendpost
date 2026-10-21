package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/search"
)

// compatInfo is GET /info. It describes the project's mailbox in the shape of
// Mailpit's application information. Fields about the Mailpit process and its
// database are present and zero, there is nothing equivalent per project.
func (s *server) compatInfo(w http.ResponseWriter, r *http.Request, c *compatCall) {
	counts, err := s.Messages.CountFiltered(r.Context(), c.scope, search.Filter{})
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	tags, err := s.Messages.TagCounts(r.Context(), c.scope)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	perTag := make(map[string]int64, len(tags))
	for _, t := range tags {
		perTag[t.Name] = t.Messages
	}
	writeCompatJSON(w, map[string]any{
		"Database":      "",
		"DatabaseSize":  0,
		"LatestVersion": "",
		"Messages":      counts.Total,
		"RuntimeStats": map[string]any{
			"Memory": 0, "MessagesDeleted": 0, "SMTPAccepted": 0, "SMTPAcceptedSize": 0,
			"SMTPIgnored": 0, "SMTPRejected": 0, "Uptime": 0,
		},
		"Tags":    perTag,
		"Unread":  counts.Unread,
		"Version": s.Info.Version,
	})
}
