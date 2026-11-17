package messages_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/authz/authztest"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/messages"
)

// fakeFeed is a subscription driven by the test.
type fakeFeed struct {
	ready  chan struct{}
	events chan events.Event
	closed chan struct{}
}

func newFakeFeed() *fakeFeed {
	return &fakeFeed{ready: make(chan struct{}), events: make(chan events.Event, 16), closed: make(chan struct{})}
}

func (f *fakeFeed) Ready() <-chan struct{}      { return f.ready }
func (f *fakeFeed) Events() <-chan events.Event { return f.events }
func (f *fakeFeed) Close()                      { close(f.closed) }

// fakeStore holds the "messages" the matcher returns and counts queries.
type fakeStore struct {
	mu      sync.Mutex
	items   []messages.Summary
	queries int
	err     error
}

func (s *fakeStore) Match(context.Context, authz.Scope, messages.MatchQuery) ([]messages.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries++
	return append([]messages.Summary(nil), s.items...), s.err
}

func (s *fakeStore) add(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, messages.Summary{ID: id})
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries
}

type waitOutcome struct {
	res messages.WaitResult
	err error
}

func startWait(ctx context.Context, w *messages.Waiter, req messages.WaitRequest) chan waitOutcome {
	out := make(chan waitOutcome, 1)
	go func() {
		res, err := w.Wait(ctx, authztest.Scope(1), req)
		out <- waitOutcome{res, err}
	}()
	return out
}

func TestWaiterSubscribesBeforeQueryingAndWakesOnEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, feed := &fakeStore{}, newFakeFeed()
		w := messages.NewWaiter(store, func(int64) messages.Feed { return feed })
		done := startWait(t.Context(), w, messages.WaitRequest{Count: 2, Deadline: time.Now().Add(time.Minute), Match: messages.MatchQuery{AfterID: 5}})

		synctest.Wait()
		if store.count() != 0 {
			t.Fatal("queried before the subscription was ready")
		}
		close(feed.ready)
		synctest.Wait()
		if store.count() != 1 {
			t.Fatalf("%d queries after ready", store.count())
		}

		// Irrelevant events cost no query: other types and ids already seen.
		feed.events <- events.Event{Type: events.TypeCommentCreated}
		feed.events <- events.Event{Type: events.TypeMessageCreated, MessageID: 3}
		synctest.Wait()
		if store.count() != 1 {
			t.Fatalf("irrelevant events queried: %d", store.count())
		}

		// A burst costs one query.
		store.add(6)
		feed.events <- events.Event{Type: events.TypeMessageCreated, MessageID: 6}
		feed.events <- events.Event{Type: events.TypeMessageCreated, MessageID: 7}
		feed.events <- events.Event{Type: events.TypeMessageCreated, MessageID: 8}
		synctest.Wait()
		if store.count() != 2 {
			t.Fatalf("burst made %d queries", store.count())
		}
		select {
		case o := <-done:
			t.Fatalf("returned early: %+v", o)
		default:
		}

		store.add(7)
		feed.events <- events.Event{Type: events.TypeResync}
		o := <-done
		if o.err != nil || o.res.TimedOut || len(o.res.Messages) != 2 {
			t.Fatalf("outcome %+v", o)
		}
		<-feed.closed
	})
}

func TestWaiterTimesOutWithPartialResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, feed := &fakeStore{}, newFakeFeed()
		store.add(1)
		close(feed.ready)
		w := messages.NewWaiter(store, func(int64) messages.Feed { return feed })
		start := time.Now()
		o := <-startWait(t.Context(), w, messages.WaitRequest{Count: 3, Deadline: start.Add(30 * time.Second)})
		if o.err != nil || !o.res.TimedOut || len(o.res.Messages) != 1 {
			t.Fatalf("outcome %+v", o)
		}
		if got := time.Since(start); got != 30*time.Second {
			t.Errorf("returned after %v", got)
		}
	})
}

func TestWaiterWithoutTimeQueriesOnce(t *testing.T) {
	store := &fakeStore{}
	store.add(1)
	w := messages.NewWaiter(store, func(int64) messages.Feed {
		t.Error("a wait without time must not subscribe")
		return newFakeFeed()
	})
	res, err := w.Wait(t.Context(), authztest.Scope(1), messages.WaitRequest{Count: 1})
	if err != nil || res.TimedOut || len(res.Messages) != 1 || store.count() != 1 {
		t.Fatalf("res %+v err %v queries %d", res, err, store.count())
	}
}

func TestWaiterErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := newFakeFeed()
		close(feed.ready)
		boom := errors.New("boom")
		w := messages.NewWaiter(&fakeStore{err: boom}, func(int64) messages.Feed { return feed })
		if _, err := w.Wait(t.Context(), authztest.Scope(1), messages.WaitRequest{Count: 1, Deadline: time.Now().Add(time.Second)}); !errors.Is(err, boom) {
			t.Errorf("query error: %v", err)
		}

		w = messages.NewWaiter(&fakeStore{}, func(int64) messages.Feed { return newFakeFeed() })
		ctx, cancel := context.WithCancel(t.Context())
		done := startWait(ctx, w, messages.WaitRequest{Count: 1, Deadline: time.Now().Add(time.Minute)})
		synctest.Wait()
		cancel()
		if o := <-done; !errors.Is(o.err, context.Canceled) {
			t.Errorf("cancel: %+v", o)
		}

		if _, err := w.Wait(t.Context(), authztest.Scope(1), messages.WaitRequest{Match: messages.MatchQuery{Run: "a\nb"}}); !errors.Is(err, messages.ErrInvalidRun) {
			t.Errorf("run validation: %v", err)
		}
	})
}
