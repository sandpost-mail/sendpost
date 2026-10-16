package smtpd

import (
	"sync"
	"time"
)

// Default per-IP authentication failure limits.
const (
	DefaultMaxAuthFailures = 10
	DefaultAuthFailWindow  = 5 * time.Minute
	pruneThreshold         = 4096
)

// FailureLimiter counts authentication failures per source IP in a sliding
// window. Once an IP reaches the limit it is blocked until its oldest failure
// leaves the window. Successful logins do not reset the counter, so one valid
// credential can not be used to keep guessing others.
type FailureLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	failures map[string][]time.Time
}

// NewFailureLimiter returns a limiter allowing max failures per window.
func NewFailureLimiter(maxFailures int, window time.Duration) *FailureLimiter {
	return newFailureLimiter(maxFailures, window, time.Now)
}

func newFailureLimiter(maxFailures int, window time.Duration, now func() time.Time) *FailureLimiter {
	return &FailureLimiter{max: maxFailures, window: window, now: now, failures: map[string][]time.Time{}}
}

// Blocked reports whether ip has used up its failure budget.
func (l *FailureLimiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= l.max
}

// Fail records one failed attempt from ip.
func (l *FailureLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) >= pruneThreshold {
		l.pruneAll()
	}
	l.failures[ip] = append(l.recent(ip), l.now())
}

// recent drops expired entries for ip and returns the live ones. Callers hold mu.
func (l *FailureLimiter) recent(ip string) []time.Time {
	cutoff := l.now().Add(-l.window)
	ts := l.failures[ip]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = ts
	return ts
}

func (l *FailureLimiter) pruneAll() {
	for ip := range l.failures {
		l.recent(ip)
	}
}
