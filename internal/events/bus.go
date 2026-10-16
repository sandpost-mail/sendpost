package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	subscriberBuffer = 32
	minBackoff       = 200 * time.Millisecond
	maxBackoff       = 10 * time.Second
)

type projectState struct {
	subs  map[*Subscription]struct{}
	ready chan struct{}
}

// Subscription receives the events of one project.
type Subscription struct {
	// C delivers events. It is never closed, stop reading after Close or when
	// your context ends.
	C <-chan Event

	ch    chan Event
	bus   *Bus
	pid   int64
	ready <-chan struct{}
}

// Ready is closed once the bus is LISTENing for the project, so events
// published afterwards are guaranteed to be delivered.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() { s.bus.unsubscribe(s) }

// Bus listens on Postgres and fans events out to local subscribers.
type Bus struct {
	dsn  string
	log  *slog.Logger
	kick chan struct{}

	mu       sync.Mutex
	projects map[int64]*projectState
}

// NewBus returns a Bus that opens its own dedicated connection from dsn.
func NewBus(dsn string, log *slog.Logger) *Bus {
	return &Bus{
		dsn:      dsn,
		log:      log,
		kick:     make(chan struct{}, 1),
		projects: make(map[int64]*projectState),
	}
}

// Subscribe registers for the events of projectID.
func (b *Bus) Subscribe(projectID int64) *Subscription {
	ch := make(chan Event, subscriberBuffer)
	sub := &Subscription{C: ch, ch: ch, bus: b, pid: projectID}
	b.mu.Lock()
	st := b.projects[projectID]
	if st == nil {
		st = &projectState{subs: map[*Subscription]struct{}{}, ready: make(chan struct{})}
		b.projects[projectID] = st
	}
	st.subs[sub] = struct{}{}
	sub.ready = st.ready
	b.mu.Unlock()
	b.wake()
	return sub
}

func (b *Bus) unsubscribe(s *Subscription) {
	b.mu.Lock()
	if st := b.projects[s.pid]; st != nil {
		delete(st.subs, s)
		if len(st.subs) == 0 {
			delete(b.projects, s.pid)
		}
	}
	b.mu.Unlock()
	b.wake()
}

func (b *Bus) wake() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Run keeps a LISTEN connection alive until ctx is cancelled, reconnecting
// with backoff. After a reconnect every subscriber receives a resync event.
func (b *Bus) Run(ctx context.Context) error {
	backoff := minBackoff
	first := true
	for ctx.Err() == nil {
		err := b.session(ctx, !first, func() { backoff = minBackoff })
		first = false
		if ctx.Err() != nil {
			break
		}
		b.log.Warn("event bus connection lost", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
	return nil
}

// session runs one connection until it fails or ctx ends.
func (b *Bus) session(ctx context.Context, resync bool, connected func()) error {
	conn, err := pgx.Connect(ctx, b.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	connected()

	listening := map[int64]*projectState{}
	if resync {
		b.broadcastAll(Event{Type: TypeResync})
	}
	for {
		if err := b.reconcile(ctx, conn, listening); err != nil {
			return err
		}
		wctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-b.kick:
				cancel()
			case <-wctx.Done():
			}
		}()
		n, err := conn.WaitForNotification(wctx)
		cancel()
		<-done
		switch {
		case err == nil:
			b.dispatch(n.Channel, n.Payload)
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.Canceled):
			// Woken by a subscription change, loop to reconcile.
		default:
			return err
		}
	}
}

// reconcile makes the LISTEN set match the subscribed projects.
func (b *Bus) reconcile(ctx context.Context, conn *pgx.Conn, listening map[int64]*projectState) error {
	b.mu.Lock()
	want := make(map[int64]*projectState, len(b.projects))
	for pid, st := range b.projects {
		want[pid] = st
	}
	b.mu.Unlock()

	for pid := range listening {
		if _, ok := want[pid]; !ok {
			if _, err := conn.Exec(ctx, "UNLISTEN "+pgx.Identifier{Channel(pid)}.Sanitize()); err != nil {
				return err
			}
			delete(listening, pid)
		}
	}
	for pid, st := range want {
		if cur, ok := listening[pid]; ok {
			if cur != st {
				// Re-subscribed while still LISTENing: the new state is live.
				listening[pid] = st
				b.markReady(pid, st)
			}
			continue
		}
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{Channel(pid)}.Sanitize()); err != nil {
			return err
		}
		listening[pid] = st
		b.markReady(pid, st)
	}
	return nil
}

func (b *Bus) markReady(pid int64, st *projectState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.projects[pid] != st {
		return
	}
	select {
	case <-st.ready:
	default:
		close(st.ready)
	}
}

func (b *Bus) dispatch(channel, payload string) {
	pid, ok := projectFromChannel(channel)
	if !ok {
		return
	}
	var ev Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		b.log.Warn("ignoring malformed event", "channel", channel)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.projects[pid]; st != nil {
		for sub := range st.subs {
			deliver(sub, ev)
		}
	}
}

func (b *Bus) broadcastAll(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, st := range b.projects {
		// The listen state is rebuilt on the new connection.
		st.ready = make(chan struct{})
		for sub := range st.subs {
			deliver(sub, ev)
		}
	}
}

// deliver never blocks. When a subscriber is too slow, its oldest event is
// dropped and a resync is queued so it reloads instead of missing data.
func deliver(sub *Subscription, ev Event) {
	select {
	case sub.ch <- ev:
		return
	default:
	}
	select {
	case <-sub.ch:
	default:
	}
	select {
	case sub.ch <- Event{Type: TypeResync}:
	default:
	}
}
