package api

import (
	"errors"
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/delivery"
)

// codeDeliveryBlocked is the error code of a release the policy engine refused.
const codeDeliveryBlocked = "delivery_blocked"

// getTransport answers {"transport": null} when the project has none. The
// password is never part of an answer, only has_secret.
func (s *server) getTransport(w http.ResponseWriter, r *http.Request, c *call) {
	t, err := s.Delivery.GetTransport(r.Context(), c.scope)
	if errorsIsNotFound(err) {
		writeJSON(w, http.StatusOK, map[string]any{"transport": nil})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transport": t})
}

func (s *server) putTransport(w http.ResponseWriter, r *http.Request, c *call) {
	var in delivery.TransportInput
	if !decodeBody(w, r, &in) {
		return
	}
	t, err := s.Delivery.PutTransport(r.Context(), c.scope, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTransport(w http.ResponseWriter, r *http.Request, c *call) {
	if err := s.Delivery.DeleteTransport(r.Context(), c.scope); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) testTransport(w http.ResponseWriter, r *http.Request, c *call) {
	res, err := s.Delivery.TestTransport(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) getPolicy(w http.ResponseWriter, r *http.Request, c *call) {
	p, err := s.Delivery.GetPolicy(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) putPolicy(w http.ResponseWriter, r *http.Request, c *call) {
	var in delivery.PolicyInput
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.Delivery.PutPolicy(r.Context(), c.scope, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) listDeliveries(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	list, err := s.Delivery.ListDeliveries(r.Context(), c.scope, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// releaseMessage queues a manual release. 202: the relay is contacted by a
// job, the rows say how it went.
func (s *server) releaseMessage(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	var in struct {
		To []string `json:"to"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	list, err := s.Delivery.Release(r.Context(), c.scope, mid, in.To)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"items": list})
}

func errorsIsNotFound(err error) bool { return errors.Is(err, delivery.ErrNotFound) }
