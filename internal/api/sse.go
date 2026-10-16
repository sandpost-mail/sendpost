package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sandpost-mail/sandpost/internal/events"
)

const keepAliveInterval = 25 * time.Second

// streamEvents is the SSE endpoint of a project. It first sends "ready" once
// the bus is LISTENing, so a client that refetches on "ready" cannot miss a
// message published in between. "resync" means events may have been missed.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request, pid int64) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.Bus == nil {
		writeError(w, http.StatusNotImplemented, "event streaming is not available")
		return
	}
	sub := s.Bus.Subscribe(pid)
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
	if !writeEvent(w, flusher, events.Event{Type: events.TypeReady}) {
		return
	}
	tick := time.NewTicker(keepAliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-sub.C:
			if !writeEvent(w, flusher, ev) {
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

func writeEvent(w http.ResponseWriter, f http.Flusher, ev events.Event) bool {
	data, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
		return false
	}
	f.Flush()
	return true
}
