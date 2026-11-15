package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sandpost-mail/sandpost/internal/events"
)

const keepAliveInterval = 25 * time.Second

// recheckInterval is how often an open stream re-runs the authentication and
// access check of its start, so a revoked token, an ended session or a
// removed project role stops the stream within that time. A variable only so
// tests can shorten it.
var recheckInterval = 30 * time.Second

// streamEvents is the SSE endpoint of a project. Access was checked by the
// guard before this runs, so no LISTEN is opened for an unauthorized caller.
// It first sends "ready" once the bus is LISTENing, so a client that refetches
// on "ready" cannot miss a message published in between. "resync" means events
// may have been missed. The stream ends when the session expires or when the
// periodic access re-check fails; the client then fails to reconnect and
// shows the login page.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request, c *call) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.Bus == nil {
		writeError(w, http.StatusNotImplemented, "event streaming is not available")
		return
	}
	release, ok := s.acquireStream(w, c)
	if !ok {
		return
	}
	defer release()
	sub := s.Bus.Subscribe(c.scope.ProjectID())
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	select {
	case <-sub.Ready():
	case <-r.Context().Done():
		return
	}
	if !s.writeEvent(w, flusher, events.Event{Type: events.TypeReady}) {
		return
	}
	tick := time.NewTicker(keepAliveInterval)
	defer tick.Stop()
	recheck := time.NewTicker(recheckInterval)
	defer recheck.Stop()
	var expired <-chan time.Time
	if !c.p.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(c.p.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expired:
			return
		case <-recheck.C:
			if !s.stillAllowed(r, c) {
				return
			}
		case ev := <-sub.C:
			if !s.writeEvent(w, flusher, ev) {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// stillAllowed re-runs the checks the guard made when the stream started:
// the credentials still authenticate the same principal, no password change
// is pending, and the principal still has the route's capability on the
// project.
func (s *server) stillAllowed(r *http.Request, c *call) bool {
	p, err := s.principal(r)
	if err != nil || p.MustChangePassword || p.UserID != c.p.UserID || p.TokenID != c.p.TokenID {
		return false
	}
	_, err = s.Authz.Project(r.Context(), p, c.scope.ProjectID(), c.cap)
	return err == nil
}

// writeEvent sends one event and reports whether the stream can go on. An
// event that can not be encoded is logged and skipped.
func (s *server) writeEvent(w http.ResponseWriter, f http.Flusher, ev events.Event) bool {
	data, err := json.Marshal(ev)
	if err != nil {
		s.Log.Error("sse marshal", "type", ev.Type, "error", err)
		return true
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
		return false
	}
	f.Flush()
	return true
}
