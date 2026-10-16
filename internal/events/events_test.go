package events

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestChannelRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 42, 9007199254740993} {
		got, ok := projectFromChannel(Channel(id))
		if !ok || got != id {
			t.Errorf("id %d: %d %v", id, got, ok)
		}
	}
	for _, bad := range []string{"", "other_1", "sp_msg_", "sp_msg_x"} {
		if _, ok := projectFromChannel(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestDeliverDropsOldestWithResync(t *testing.T) {
	ch := make(chan Event, 2)
	sub := &Subscription{ch: ch}
	deliver(sub, Event{Type: TypeMessageCreated, MessageID: 1})
	deliver(sub, Event{Type: TypeMessageCreated, MessageID: 2})
	deliver(sub, Event{Type: TypeMessageCreated, MessageID: 3}) // overflow
	if len(ch) != 2 {
		t.Fatalf("buffer %d", len(ch))
	}
	var sawResync bool
	for len(ch) > 0 {
		if (<-ch).Type == TypeResync {
			sawResync = true
		}
	}
	if !sawResync {
		t.Error("overflow must queue a resync")
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func recv(t *testing.T, sub *Subscription) Event {
	t.Helper()
	select {
	case ev := <-sub.C:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func expectNone(t *testing.T, sub *Subscription) {
	t.Helper()
	select {
	case ev := <-sub.C:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func waitReady(t *testing.T, sub *Subscription) {
	t.Helper()
	select {
	case <-sub.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("subscription never became ready")
	}
}

func startBus(t *testing.T, dsn string) *Bus {
	t.Helper()
	bus := NewBus(dsn, discard())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return bus
}

func TestBusFanOutAndIsolation(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	bus := startBus(t, dsn)

	a1, a2, b := bus.Subscribe(1), bus.Subscribe(1), bus.Subscribe(2)
	for _, s := range []*Subscription{a1, a2, b} {
		waitReady(t, s)
	}

	if err := Notify(ctx, pool, 1, Event{Type: TypeMessageCreated, MessageID: 7}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Subscription{a1, a2} {
		if ev := recv(t, s); ev.Type != TypeMessageCreated || ev.MessageID != 7 {
			t.Errorf("got %+v", ev)
		}
	}
	expectNone(t, b)

	// Notifications inside a rolled back transaction are never delivered.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Notify(ctx, tx, 1, Event{Type: TypeMessageCreated, MessageID: 8}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	expectNone(t, a1)

	// After unsubscribing, the channel is released and re-subscribing works.
	a1.Close()
	a1.Close()
	a2.Close()
	again := bus.Subscribe(1)
	waitReady(t, again)
	if err := Notify(ctx, pool, 1, Event{Type: TypeMessageCreated, MessageID: 9}); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, again); ev.MessageID != 9 {
		t.Errorf("got %+v", ev)
	}
}

func TestBusIgnoresMalformedPayload(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	bus := startBus(t, dsn)
	sub := bus.Subscribe(3)
	waitReady(t, sub)
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, 'not json')", Channel(3)); err != nil {
		t.Fatal(err)
	}
	expectNone(t, sub)
	if err := Notify(ctx, pool, 3, Event{Type: TypeMessageCreated, MessageID: 1}); err != nil {
		t.Fatal(err)
	}
	recv(t, sub)
}

func TestBusReconnectsAndSendsResync(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	bus := startBus(t, dsn)
	sub := bus.Subscribe(5)
	waitReady(t, sub)

	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND query LIKE 'LISTEN%' AND datname = current_database()`); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, sub); ev.Type != TypeResync {
		t.Fatalf("want resync, got %+v", ev)
	}
	// Ready is re-armed on the new connection, poll until delivery works again.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := Notify(ctx, pool, 5, Event{Type: TypeMessageCreated, MessageID: 11}); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-sub.C:
			if ev.MessageID == 11 {
				return
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatal("no event after reconnect")
}

func TestNotifyError(t *testing.T) {
	pool := testutil.Pool(t)
	pool.Close()
	if err := Notify(context.Background(), pool, 1, Event{Type: "x"}); err == nil {
		t.Error("expected error")
	}
}

func TestRunStopsWhenDatabaseUnreachable(t *testing.T) {
	bus := NewBus("postgres://u:p@127.0.0.1:1/db?connect_timeout=1", discard())
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if err := bus.Run(ctx); err != nil {
		t.Fatalf("Run must only return on cancel without error: %v", err)
	}
}
