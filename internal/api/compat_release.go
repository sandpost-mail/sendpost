package api

import "net/http"

// compatRelease is POST /message/{ID}/release. The body is {"To": [...]}.
// Like the native release it only sends to addresses on the project's
// allowlist through the project's enabled transport, and answers 400 with the
// reason otherwise. The mail is sent by a job, "ok" means it was queued.
func (s *server) compatRelease(w http.ResponseWriter, r *http.Request, c *compatCall) {
	id, ok := s.compatMessageID(w, r, c, r.PathValue("ID"))
	if !ok {
		return
	}
	var body struct{ To []string }
	if !compatBody(w, r, &body) {
		return
	}
	if len(body.To) == 0 {
		compatText(w, http.StatusBadRequest, "To is required")
		return
	}
	if _, err := s.Delivery.Release(r.Context(), c.scope, id, body.To); err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}
