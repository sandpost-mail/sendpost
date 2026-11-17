package delivery

import (
	"errors"
	"sync"
	"time"
)

// Connection test limits: an owner can test a transport a few times a minute,
// enough to fix a typo, too few to scan a network through the relay dialer.
const (
	maxTransportTests    = 5
	transportTestWindow  = time.Minute
	limiterPruneAtLength = 4096
)

// ErrRateLimited is returned when the connection test was used too often.
// errors.As with *RateLimitError gives the wait.
var ErrRateLimited = errors.New("too many connection tests")

// RateLimitError says how long to wait before the next connection test.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }

// Is makes errors.Is(err, ErrRateLimited) true.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// windowLimiter allows max events per key in a sliding window.
type windowLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	events map[int64][]time.Time
}

func newWindowLimiter(maxEvents int, window time.Duration, now func() time.Time) *windowLimiter {
	return &windowLimiter{max: maxEvents, window: window, now: now, events: map[int64][]time.Time{}}
}

// take records one event for key, or returns how long to wait when the
// budget is used up (nothing is recorded then).
func (l *windowLimiter) take(key int64) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.events) >= limiterPruneAtLength {
		for k := range l.events {
			l.recent(k, now)
		}
	}
	ts := l.recent(key, now)
	if len(ts) >= l.max {
		return ts[0].Add(l.window).Sub(now), false
	}
	l.events[key] = append(ts, now)
	return 0, true
}

// recent drops expired events of key and returns the live ones. Callers hold mu.
func (l *windowLimiter) recent(key int64, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	ts := l.events[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	if i == len(ts) {
		delete(l.events, key)
		return nil
	}
	l.events[key] = ts[i:]
	return ts[i:]
}
