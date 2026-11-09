package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

// Wait endpoint limits.
const (
	// runHeader isolates a CI run, see runID.
	runHeader      = "X-Run-Id"
	defaultWaitSec = 30
	maxWaitSec     = 120
	maxWaitCount   = 50
)

// waitResponse is the answer of the wait endpoint. Messages are the matches
// found, oldest first. TimedOut is true when fewer than count matched before
// the timeout, Messages then holds what did match (possibly nothing).
type waitResponse struct {
	Messages []messages.Summary `json:"messages"`
	TimedOut bool               `json:"timed_out"`
}

// waitParams are the validated query parameters of the wait endpoint.
type waitParams struct {
	match   messages.MatchQuery
	count   int
	timeout time.Duration
}

// runID returns the run id of a request: the X-Run-Id header or the "run"
// query parameter. Both may be given only when equal.
func runID(r *http.Request) (string, error) {
	h, q := r.Header.Get(runHeader), r.URL.Query().Get("run")
	if h != "" && q != "" && h != q {
		return "", errors.New("X-Run-Id header and run parameter differ")
	}
	run := h
	if run == "" {
		run = q
	}
	if err := messages.ValidateRunID(run); err != nil {
		return "", err
	}
	return run, nil
}

func parseWait(r *http.Request) (waitParams, error) {
	vals := r.URL.Query()
	q, err := search.Parse(vals.Get("q"))
	if err != nil {
		return waitParams{}, err
	}
	run, err := runID(r)
	if err != nil {
		return waitParams{}, err
	}
	p := waitParams{
		match:   messages.MatchQuery{Query: q, Run: run, Limit: messages.MaxLimit},
		count:   1,
		timeout: defaultWaitSec * time.Second,
	}
	if p.match.AfterID, err = intParam(vals.Get("after_id"), "after_id", 0, 1<<62, 0); err != nil {
		return waitParams{}, err
	}
	count, err := intParam(vals.Get("count"), "count", 1, maxWaitCount, 1)
	if err != nil {
		return waitParams{}, err
	}
	p.count = int(count)
	secs, err := intParam(vals.Get("timeout"), "timeout", 0, maxWaitSec, defaultWaitSec)
	if err != nil {
		return waitParams{}, err
	}
	p.timeout = time.Duration(secs) * time.Second
	return p, nil
}

// intParam parses an optional integer query parameter within [lo, hi].
func intParam(raw, name string, lo, hi, def int64) (int64, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < lo || n > hi {
		return 0, errors.New("invalid " + name)
	}
	return n, nil
}

// waitMessages is a long poll on the event bus: it answers as soon as at
// least count messages of the project match the filters, or when the timeout
// ends. Messages that arrived before the call count too (the usual CI order is
// "trigger the mail, then wait"), after_id skips the ones already seen. The
// subscription is opened and live before the first query, so a message
// committed in between is never missed: it is either in the query result or
// delivered as an event.
func (s *server) waitMessages(w http.ResponseWriter, r *http.Request, c *call) {
	if s.Bus == nil {
		writeError(w, http.StatusNotImplemented, "waiting is not available")
		return
	}
	p, err := parseWait(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.timeout == 0 {
		s.answerWait(w, r, c, p, true)
		return
	}
	deadline := time.Now().Add(p.timeout)
	if !c.p.ExpiresAt.IsZero() && c.p.ExpiresAt.Before(deadline) {
		deadline = c.p.ExpiresAt
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	sub := s.Bus.Subscribe(c.scope.ProjectID())
	defer sub.Close()
	select {
	case <-sub.Ready():
	case <-timer.C:
		s.answerWait(w, r, c, p, true)
		return
	case <-r.Context().Done():
		return
	}
	for {
		items, err := s.Messages.Match(r.Context(), c.scope, p.match)
		if err != nil {
			s.failWait(w, r, err)
			return
		}
		if len(items) >= p.count {
			writeJSON(w, http.StatusOK, waitResponse{Messages: items})
			return
		}
		if !waitForEvent(r, sub, timer) {
			if r.Context().Err() == nil {
				writeJSON(w, http.StatusOK, waitResponse{Messages: items, TimedOut: true})
			}
			return
		}
	}
}

// waitForEvent blocks until the project has news (a new message, or a resync)
// and reports false on timeout or when the client went away. Queued events are
// drained so a burst costs one query.
func waitForEvent(r *http.Request, sub *events.Subscription, timer *time.Timer) bool {
	for {
		select {
		case ev := <-sub.C:
			if ev.Type != events.TypeMessageCreated && ev.Type != events.TypeResync {
				continue
			}
			for {
				select {
				case <-sub.C:
				default:
					return true
				}
			}
		case <-timer.C:
			return false
		case <-r.Context().Done():
			return false
		}
	}
}

// answerWait runs one query without waiting: a zero timeout, or a call that ran
// out of time before the subscription was live.
func (s *server) answerWait(w http.ResponseWriter, r *http.Request, c *call, p waitParams, timedOut bool) {
	items, err := s.Messages.Match(r.Context(), c.scope, p.match)
	if err != nil {
		s.failWait(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, waitResponse{Messages: items, TimedOut: timedOut && len(items) < p.count})
}

func (s *server) failWait(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, messages.ErrInvalidRun) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.fail(w, r, err)
}
