package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB stands in for Postgres: it hands out fakeConns, remembers which
// channels the live one LISTENs on, and can drop it or refuse new dials.
type fakeDB struct {
	mu        sync.Mutex
	conn      *fakeConn
	dials     int
	refuse    bool
	execs     []string
	gate      chan struct{} // when set, the next Exec blocks until it is closed
	execBlock chan struct{} // closed when an Exec is blocked on gate
}

type fakeConn struct {
	db        *fakeDB
	listening map[string]bool
	notes     chan *pgconn.Notification
	dead      chan struct{}
	closeOnce sync.Once
}

var errConnLost = errors.New("fake: connection lost")

func (db *fakeDB) dial(context.Context) (listenConn, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.refuse {
		return nil, errors.New("fake: connection refused")
	}
	db.dials++
	db.conn = &fakeConn{
		db:        db,
		listening: map[string]bool{},
		notes:     make(chan *pgconn.Notification, 256),
		dead:      make(chan struct{}),
	}
	return db.conn, nil
}

func (c *fakeConn) Exec(ctx context.Context, sql string) error {
	c.db.mu.Lock()
	gate, blocked := c.db.gate, c.db.execBlock
	c.db.gate, c.db.execBlock = nil, nil
	c.db.mu.Unlock()
	if gate != nil {
		close(blocked)
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-c.dead:
		return errConnLost
	default:
	}
	verb, quoted, _ := strings.Cut(sql, " ")
	ch := strings.Trim(quoted, `"`)
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.execs = append(c.db.execs, verb+" "+ch)
	switch verb {
	case "LISTEN":
		c.listening[ch] = true
	case "UNLISTEN":
		delete(c.listening, ch)
	default:
		return fmt.Errorf("fake: unexpected sql %q", sql)
	}
	return nil
}

func (c *fakeConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case n := <-c.notes:
		return n, nil
	case <-c.dead:
		return nil, errConnLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *fakeConn) Close(context.Context) error {
	c.closeOnce.Do(func() { close(c.dead) })
	return nil
}

// notify delivers like pg_notify: only when the live connection LISTENs.
func (db *fakeDB) notify(t *testing.T, pid int64, ev Event) {
	t.Helper()
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	db.notifyRaw(pid, string(payload))
}

func (db *fakeDB) notifyRaw(pid int64, payload string) {
	db.mu.Lock()
	c := db.conn
	live := c != nil && c.listening[Channel(pid)]
	db.mu.Unlock()
	if !live {
		return
	}
	// Send outside the lock so a full queue never blocks the bus loop's Exec.
	select {
	case c.notes <- &pgconn.Notification{Channel: Channel(pid), Payload: payload}:
	case <-c.dead:
	}
}

// drop kills the live connection, as a Postgres restart would.
func (db *fakeDB) drop() {
	db.mu.Lock()
	c := db.conn
	db.conn = nil
	db.mu.Unlock()
	if c != nil {
		_ = c.Close(context.Background())
	}
}

func (db *fakeDB) listened() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.conn == nil {
		return nil
	}
	out := []string{}
	for ch := range db.conn.listening {
		out = append(out, ch)
	}
	return out
}

func (db *fakeDB) execLog() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.execs...)
}

func (db *fakeDB) setRefuse(v bool) {
	db.mu.Lock()
	db.refuse = v
	db.mu.Unlock()
}

// startFakeBus runs a bus against a fakeDB inside the current synctest
// bubble and stops it when the test ends.
func startFakeBus(t *testing.T) (*Bus, *fakeDB) {
	t.Helper()
	db := &fakeDB{}
	bus := newBus(db.dial, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return bus, db
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func drain(sub *Subscription) []Event {
	var out []Event
	for {
		select {
		case ev := <-sub.C:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestFakeBusDeliversAndIsolates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		a, b := bus.Subscribe(1), bus.Subscribe(2)
		synctest.Wait()
		if !isClosed(a.Ready()) || !isClosed(b.Ready()) {
			t.Fatal("subscriptions must be ready once LISTENing")
		}
		db.notify(t, 1, Event{Type: TypeMessageCreated, MessageID: 7})
		db.notifyRaw(1, "not json")
		synctest.Wait()
		if got := drain(a); len(got) != 1 || got[0].MessageID != 7 {
			t.Errorf("a got %+v", got)
		}
		if got := drain(b); len(got) != 0 {
			t.Errorf("event leaked to another project: %+v", got)
		}
	})
}

func TestFakeBusUnlistensWhenLastSubscriberLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		a1, a2 := bus.Subscribe(1), bus.Subscribe(1)
		synctest.Wait()
		a1.Close()
		a1.Close() // idempotent
		synctest.Wait()
		if got := db.listened(); len(got) != 1 {
			t.Fatalf("still one subscriber, listening %v", got)
		}
		a2.Close()
		synctest.Wait()
		if got := db.listened(); len(got) != 0 {
			t.Fatalf("no subscriber left, still listening %v", got)
		}
		again := bus.Subscribe(1)
		synctest.Wait()
		if !isClosed(again.Ready()) {
			t.Fatal("re-subscription never became ready")
		}
		db.notify(t, 1, Event{Type: TypeMessageCreated, MessageID: 9})
		synctest.Wait()
		if got := drain(again); len(got) != 1 || got[0].MessageID != 9 {
			t.Errorf("got %+v", got)
		}
		want := []string{"LISTEN sp_msg_1", "UNLISTEN sp_msg_1", "LISTEN sp_msg_1"}
		if got := db.execLog(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("execs %v want %v", got, want)
		}
	})
}

// A project unsubscribed and subscribed again before the loop reconciles is
// still LISTENed: its new state must become ready without a new LISTEN.
func TestFakeBusResubscribeBeforeReconcile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		first := bus.Subscribe(1)
		synctest.Wait()

		// Hold the loop inside reconcile (LISTEN for project 2) while project
		// 1 churns, so the next reconcile sees a new state for a live LISTEN.
		gate, blocked := make(chan struct{}), make(chan struct{})
		db.mu.Lock()
		db.gate, db.execBlock = gate, blocked
		db.mu.Unlock()
		other := bus.Subscribe(2)
		<-blocked
		first.Close()
		second := bus.Subscribe(1)
		if isClosed(second.Ready()) {
			t.Fatal("a fresh state must not be ready before the loop sees it")
		}
		close(gate)
		synctest.Wait()

		if !isClosed(second.Ready()) || !isClosed(other.Ready()) {
			t.Fatal("re-subscribed project never became ready")
		}
		want := []string{"LISTEN sp_msg_1", "LISTEN sp_msg_2"}
		if got := db.execLog(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("execs %v want %v", got, want)
		}
		db.notify(t, 1, Event{Type: TypeMessageCreated, MessageID: 3})
		synctest.Wait()
		if got := drain(second); len(got) != 1 || got[0].MessageID != 3 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestFakeBusReadyWaitsForReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		old := bus.Subscribe(5)
		synctest.Wait()
		if !isClosed(old.Ready()) {
			t.Fatal("not ready")
		}

		db.setRefuse(true)
		db.drop()
		synctest.Wait()
		down := bus.Subscribe(5)
		fresh := bus.Subscribe(6)
		time.Sleep(time.Minute) // several failed reconnect attempts
		synctest.Wait()
		if isClosed(down.Ready()) || isClosed(fresh.Ready()) {
			t.Fatal("Ready must not report listening while the connection is down")
		}

		db.setRefuse(false)
		time.Sleep(maxBackoff)
		synctest.Wait()
		if !isClosed(down.Ready()) || !isClosed(fresh.Ready()) {
			t.Fatal("subscriptions must become ready after the reconnect")
		}
		if got := drain(old); len(got) != 1 || got[0].Type != TypeResync {
			t.Errorf("the old subscription must get a resync, got %+v", got)
		}
		db.notify(t, 5, Event{Type: TypeMessageCreated, MessageID: 11})
		synctest.Wait()
		if got := drain(old); len(got) != 1 || got[0].MessageID != 11 {
			t.Errorf("no delivery after reconnect: %+v", got)
		}
	})
}

func TestFakeBusBackoffResetsAfterConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, db := startFakeBus(t)
		synctest.Wait()
		db.drop()
		time.Sleep(minBackoff)
		synctest.Wait()
		db.mu.Lock()
		dials := db.dials
		db.mu.Unlock()
		if dials != 2 {
			t.Fatalf("dials %d, want a reconnect after minBackoff", dials)
		}
	})
}

func TestFakeBusOverflowQueuesResync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		slow := bus.Subscribe(1)
		synctest.Wait()
		for i := range subscriberBuffer + 10 {
			db.notify(t, 1, Event{Type: TypeMessageCreated, MessageID: int64(i + 1)})
		}
		synctest.Wait()
		got := drain(slow)
		if len(got) != subscriberBuffer {
			t.Fatalf("buffered %d events, want %d", len(got), subscriberBuffer)
		}
		if got[len(got)-1].Type != TypeResync {
			t.Errorf("last event %+v, want a resync after overflow", got[len(got)-1])
		}
	})
}

func TestFakeBusConcurrentChurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus, db := startFakeBus(t)
		const workers, rounds = 16, 50
		var wg sync.WaitGroup
		for w := range workers {
			wg.Go(func() {
				pid := int64(w % 4)
				for range rounds {
					sub := bus.Subscribe(pid)
					db.notify(t, pid, Event{Type: TypeMessageCreated, MessageID: 1})
					drain(sub)
					sub.Close()
				}
			})
		}
		wg.Wait()
		synctest.Wait()
		if got := db.listened(); len(got) != 0 {
			t.Fatalf("every subscriber left, still listening %v", got)
		}
		final := bus.Subscribe(1)
		synctest.Wait()
		if !isClosed(final.Ready()) {
			t.Fatal("bus wedged after churn")
		}
	})
}

func TestNewBusNilLogger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := newBus(func(context.Context) (listenConn, error) { return nil, errors.New("down") }, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := bus.Run(ctx); err != nil { // logs a warning per failed attempt
			t.Fatal(err)
		}
	})
}
