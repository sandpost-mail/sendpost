// Package ratelimit counts failures per key in a sliding window with bounded
// memory. It is used to throttle failed logins on the SMTP and HTTP edges.
package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxKeys bounds how many keys a Limiter tracks at once.
const DefaultMaxKeys = 65536

// expiredPerCall bounds the opportunistic cleanup done by each call, so no
// call scans the whole table.
const expiredPerCall = 2

// Limiter counts failures per key in a sliding window. Once a key reaches the
// limit it is blocked until its oldest failure leaves the window. Successes do
// not reset the counter, so one valid credential can not be used to keep
// guessing others.
//
// Memory is bounded: each key keeps at most max timestamps, and at most
// maxKeys keys are tracked. When the table is full the least recently touched
// key is evicted. Every operation is O(1) amortised.
type Limiter struct {
	max     int
	window  time.Duration
	maxKeys int
	now     func() time.Time

	mu    sync.Mutex
	keys  map[string]*list.Element
	order *list.List // of *entry, least recently touched first
}

type entry struct {
	key   string
	times []time.Time // oldest first, len <= max
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(l *Limiter) { l.now = now } }

// WithMaxKeys sets how many keys are tracked at most. Values below 1 are ignored.
func WithMaxKeys(n int) Option {
	return func(l *Limiter) {
		if n > 0 {
			l.maxKeys = n
		}
	}
}

// New returns a limiter allowing maxFailures per window and key. maxFailures
// below 1 is treated as 1.
func New(maxFailures int, window time.Duration, opts ...Option) *Limiter {
	l := &Limiter{
		max: max(maxFailures, 1), window: window, maxKeys: DefaultMaxKeys, now: time.Now,
		keys: map[string]*list.Element{}, order: list.New(),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Window is the sliding window length.
func (l *Limiter) Window() time.Duration { return l.window }

// Blocked reports whether key has used up its failure budget.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanup(now)
	e := l.live(key, now)
	return e != nil && len(e.times) >= l.max
}

// RetryAfter returns how long until key is no longer blocked, 0 if it is not.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.live(key, now)
	if e == nil || len(e.times) < l.max {
		return 0
	}
	// The key unblocks when enough failures have expired to drop below max.
	return e.times[len(e.times)-l.max].Add(l.window).Sub(now)
}

// Fail records one failure for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanup(now)
	l.record(key, now)
}

// Reservation is a failure recorded ahead of an attempt. Release it when the
// attempt turns out not to be a failure.
type Reservation struct {
	l   *Limiter
	key string
	at  time.Time
}

// Reserve atomically checks that key is not blocked and records a failure for
// it. It returns false, and records nothing, when key is blocked. Reserving
// before the slow check closes the race where parallel attempts all pass a
// separate Blocked call before any failure is recorded.
func (l *Limiter) Reserve(key string) (Reservation, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanup(now)
	if e := l.live(key, now); e != nil && len(e.times) >= l.max {
		return Reservation{}, false
	}
	l.record(key, now)
	return Reservation{l: l, key: key, at: now}, true
}

// Release removes the reserved failure. It is a no-op on a zero Reservation,
// when called twice, or when the entry expired or was evicted meanwhile.
func (r *Reservation) Release() {
	if r.l == nil {
		return
	}
	l := r.l
	r.l = nil
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.keys[r.key]
	if !ok {
		return
	}
	e := el.Value.(*entry)
	for i := len(e.times) - 1; i >= 0; i-- {
		if e.times[i].Equal(r.at) {
			e.times = append(e.times[:i:i], e.times[i+1:]...)
			break
		}
	}
	if len(e.times) == 0 {
		l.remove(el)
	}
}

// live returns key's entry with expired timestamps dropped, or nil. Callers hold mu.
func (l *Limiter) live(key string, now time.Time) *entry {
	el, ok := l.keys[key]
	if !ok {
		return nil
	}
	e := el.Value.(*entry)
	e.times = l.trim(e.times, now)
	if len(e.times) == 0 {
		l.remove(el)
		return nil
	}
	return e
}

func (l *Limiter) trim(ts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// record appends a failure, keeping at most max timestamps. Callers hold mu.
func (l *Limiter) record(key string, now time.Time) {
	if el, ok := l.keys[key]; ok {
		e := el.Value.(*entry)
		e.times = append(l.trim(e.times, now), now)
		if len(e.times) > l.max {
			e.times = e.times[len(e.times)-l.max:]
		}
		l.order.MoveToBack(el)
		return
	}
	for len(l.keys) >= l.maxKeys {
		l.remove(l.order.Front())
	}
	l.keys[key] = l.order.PushBack(&entry{key: key, times: []time.Time{now}})
}

// cleanup drops a few least recently touched entries whose failures all
// expired. Callers hold mu.
func (l *Limiter) cleanup(now time.Time) {
	for range expiredPerCall {
		el := l.order.Front()
		if el == nil {
			return
		}
		e := el.Value.(*entry)
		if len(l.trim(e.times, now)) != 0 {
			return
		}
		l.remove(el)
	}
}

func (l *Limiter) remove(el *list.Element) {
	delete(l.keys, el.Value.(*entry).key)
	l.order.Remove(el)
}

// Len returns the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
