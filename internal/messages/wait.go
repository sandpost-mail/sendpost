package messages

import (
	"context"
	"fmt"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/events"
)

// Feed is a live subscription to the events of one project.
type Feed interface {
	// Ready is closed once events are being delivered.
	Ready() <-chan struct{}
	// Events delivers the project's events.
	Events() <-chan events.Event
	Close()
}

// Matcher runs the wait query, Repository.Match in production.
type Matcher interface {
	Match(ctx context.Context, scope authz.Scope, q MatchQuery) ([]Summary, error)
}

// Waiter implements "wait until N messages match" on top of the event bus.
type Waiter struct {
	match     Matcher
	subscribe func(projectID int64) Feed
}

// NewWaiter returns a Waiter querying m and woken by subscribe's feeds.
func NewWaiter(m Matcher, subscribe func(projectID int64) Feed) *Waiter {
	return &Waiter{match: m, subscribe: subscribe}
}

// WaitRequest is one wait: the query, how many matches end it, and when it
// gives up. A zero Deadline (or one in the past) runs the query once.
type WaitRequest struct {
	Match    MatchQuery
	Count    int
	Deadline time.Time
}

// WaitResult holds the matches found. TimedOut is true when fewer than Count
// matched before the deadline, Messages then holds what did match.
type WaitResult struct {
	Messages []Summary
	TimedOut bool
}

// Wait answers as soon as at least req.Count messages of the project match,
// or at the deadline. Messages that arrived before the call count too,
// AfterID skips the ones already seen. The subscription is live before the
// first query, so a message committed in between is never missed: it is
// either in the query result or delivered as an event. It returns ctx.Err()
// when ctx ends first.
func (w *Waiter) Wait(ctx context.Context, scope authz.Scope, req WaitRequest) (WaitResult, error) {
	if err := ValidateRunID(req.Match.Run); err != nil {
		return WaitResult{}, err
	}
	if !time.Now().Before(req.Deadline) {
		return w.once(ctx, scope, req)
	}
	timer := time.NewTimer(time.Until(req.Deadline))
	defer timer.Stop()

	feed := w.subscribe(scope.ProjectID())
	defer feed.Close()
	select {
	case <-feed.Ready():
	case <-timer.C:
		return w.once(ctx, scope, req)
	case <-ctx.Done():
		return WaitResult{}, fmt.Errorf("wait: %w", ctx.Err())
	}
	for {
		items, err := w.match.Match(ctx, scope, req.Match)
		if err != nil {
			return WaitResult{}, err
		}
		if len(items) >= req.Count {
			return WaitResult{Messages: items}, nil
		}
		woke, err := nextRelevant(ctx, feed, timer, req.Match.AfterID)
		if err != nil {
			return WaitResult{}, err
		}
		if !woke {
			return WaitResult{Messages: items, TimedOut: true}, nil
		}
	}
}

// once runs the query a single time, for a wait that has no time left.
func (w *Waiter) once(ctx context.Context, scope authz.Scope, req WaitRequest) (WaitResult, error) {
	items, err := w.match.Match(ctx, scope, req.Match)
	if err != nil {
		return WaitResult{}, err
	}
	return WaitResult{Messages: items, TimedOut: len(items) < req.Count}, nil
}

// nextRelevant blocks until an event that can change the result arrives (a
// new message above afterID, or a resync) and reports false at the deadline.
// Events of other kinds do not cost a query, and a queued burst is drained
// so it costs one.
func nextRelevant(ctx context.Context, feed Feed, timer *time.Timer, afterID int64) (bool, error) {
	for {
		select {
		case ev := <-feed.Events():
			if !relevant(ev, afterID) {
				continue
			}
			drain(feed)
			return true, nil
		case <-timer.C:
			return false, nil
		case <-ctx.Done():
			return false, fmt.Errorf("wait: %w", ctx.Err())
		}
	}
}

func relevant(ev events.Event, afterID int64) bool {
	switch ev.Type {
	case events.TypeResync:
		return true
	case events.TypeMessageCreated:
		return ev.MessageID == 0 || ev.MessageID > afterID
	}
	return false
}

func drain(feed Feed) {
	for {
		select {
		case <-feed.Events():
		default:
			return
		}
	}
}
