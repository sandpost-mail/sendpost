package smtpd

import (
	"testing"
	"time"
)

func TestFailureLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newFailureLimiter(3, time.Minute, func() time.Time { return now })

	if l.Blocked("1.1.1.1") {
		t.Fatal("fresh ip must not be blocked")
	}
	for i := 0; i < 3; i++ {
		l.Fail("1.1.1.1")
		now = now.Add(10 * time.Second)
	}
	if !l.Blocked("1.1.1.1") {
		t.Error("3 failures must block")
	}
	if l.Blocked("2.2.2.2") {
		t.Error("other ips are independent")
	}
	// The oldest failure (t=1000) leaves the window at t=1060, we are at 1030.
	now = time.Unix(1061, 0)
	if l.Blocked("1.1.1.1") {
		t.Error("block must lift as failures age out")
	}
	now = now.Add(time.Hour)
	if l.Blocked("1.1.1.1") {
		t.Error("expired")
	}
	if len(l.failures) != 0 {
		t.Errorf("expired entries must be dropped: %d", len(l.failures))
	}
}

func TestFailureLimiterPrunesStaleIPs(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newFailureLimiter(5, time.Minute, func() time.Time { return now })
	for i := 0; i < pruneThreshold; i++ {
		l.Fail(string(rune(i)) + "ip")
	}
	now = now.Add(time.Hour)
	l.Fail("fresh")
	if len(l.failures) != 1 {
		t.Errorf("stale ips must be pruned, have %d", len(l.failures))
	}
}
