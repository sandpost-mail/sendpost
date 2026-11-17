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
// found, oldest first (newest first with order=desc). TimedOut is true when
// fewer than count matched before the timeout, Messages then holds what did
// match (possibly nothing).
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

// parseOrder reads the order parameter: asc (default, oldest first) or desc
// (newest first, so the first message is the latest match).
func parseOrder(raw string) (newestFirst bool, err error) {
	switch raw {
	case "", "asc":
		return false, nil
	case "desc":
		return true, nil
	}
	return false, errors.New("invalid order, use asc or desc")
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
	newest, err := parseOrder(vals.Get("order"))
	if err != nil {
		return waitParams{}, err
	}
	p := waitParams{
		match:   messages.MatchQuery{Query: q, Run: run, Limit: messages.MaxLimit, NewestFirst: newest},
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

// waitMessages is the long poll of the wait use case (messages.Waiter): it
// parses the parameters, caps the deadline at the session expiry and writes
// the result.
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
	release, ok := s.acquireStream(w, c)
	if !ok {
		return
	}
	defer release()
	var deadline time.Time
	if p.timeout > 0 {
		deadline = time.Now().Add(p.timeout)
		if !c.p.ExpiresAt.IsZero() && c.p.ExpiresAt.Before(deadline) {
			deadline = c.p.ExpiresAt
		}
	}
	waiter := messages.NewWaiter(s.Messages, busFeed(s.Bus))
	res, err := waiter.Wait(r.Context(), c.scope, messages.WaitRequest{Match: p.match, Count: p.count, Deadline: deadline})
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, waitResponse{Messages: res.Messages, TimedOut: res.TimedOut})
}

// busFeed adapts the event bus to the feed the wait use case consumes.
func busFeed(b *events.Bus) func(int64) messages.Feed {
	return func(pid int64) messages.Feed { return busSubscription{b.Subscribe(pid)} }
}

type busSubscription struct{ *events.Subscription }

func (s busSubscription) Events() <-chan events.Event { return s.C }
