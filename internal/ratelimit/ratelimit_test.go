package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestSlidingWindow(t *testing.T) {
	clk := newClock()
	l := New(3, time.Minute, WithClock(clk.Now))

	if l.Blocked("a") {
		t.Fatal("fresh key must not be blocked")
	}
	for range 3 {
		l.Fail("a")
		clk.Advance(10 * time.Second)
	}
	if !l.Blocked("a") {
		t.Error("3 failures must block")
	}
	if l.Blocked("b") {
		t.Error("keys are independent")
	}
	// Failures at 1000, 1010, 1020; now 1030. The first leaves at 1060.
	if got := l.RetryAfter("a"); got != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", got)
	}
	clk.Advance(31 * time.Second)
	if l.Blocked("a") {
		t.Error("block must lift as failures age out")
	}
	if got := l.RetryAfter("a"); got != 0 {
		t.Errorf("RetryAfter when not blocked = %v", got)
	}
	clk.Advance(time.Hour)
	if l.Blocked("a") || l.Len() != 0 {
		t.Errorf("expired entries must be dropped, have %d", l.Len())
	}
}

func TestReserve(t *testing.T) {
	t.Run("reservations count until released", func(t *testing.T) {
		l := New(2, time.Minute, WithClock(newClock().Now))
		r1, ok1 := l.Reserve("k")
		_, ok2 := l.Reserve("k")
		if !ok1 || !ok2 {
			t.Fatal("first two reservations must succeed")
		}
		if _, ok := l.Reserve("k"); ok {
			t.Fatal("third reservation must be refused")
		}
		r1.Release()
		r1.Release() // idempotent
		if l.Blocked("k") || l.Len() != 1 {
			t.Fatal("one failure must remain")
		}
		if _, ok := l.Reserve("k"); !ok {
			t.Error("a released reservation frees its slot")
		}
	})
	t.Run("release of the only failure drops the key", func(t *testing.T) {
		l := New(2, time.Minute, WithClock(newClock().Now))
		r, _ := l.Reserve("k")
		r.Release()
		if l.Len() != 0 {
			t.Errorf("Len = %d", l.Len())
		}
	})
	t.Run("zero reservation", func(*testing.T) {
		var r Reservation
		r.Release()
	})
	t.Run("failures beyond the limit are capped", func(t *testing.T) {
		clk := newClock()
		l := New(2, time.Minute, WithClock(clk.Now))
		for range 100 {
			l.Fail("k")
			clk.Advance(time.Second)
		}
		el := l.keys["k"]
		if n := len(el.Value.(*entry).times); n != 2 {
			t.Errorf("kept %d timestamps, want 2", n)
		}
		if got := l.RetryAfter("k"); got != 58*time.Second {
			t.Errorf("RetryAfter = %v, want the newest failures to count", got)
		}
	})
}

func TestBoundedKeys(t *testing.T) {
	clk := newClock()
	l := New(5, time.Minute, WithClock(clk.Now), WithMaxKeys(3))
	for i := range 10 {
		l.Fail(fmt.Sprint(i))
	}
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3", l.Len())
	}
	for _, k := range []string{"7", "8", "9"} {
		if _, ok := l.keys[k]; !ok {
			t.Errorf("most recent key %s must be kept", k)
		}
	}
	// Expired keys are cleaned up opportunistically.
	clk.Advance(time.Hour)
	l.Fail("x")
	l.Blocked("y")
	if l.Len() != 1 {
		t.Errorf("Len after expiry = %d, want 1", l.Len())
	}
}

// TestConcurrentReserveNeverExceedsBudget runs under -race: parallel
// reservations on one key never exceed the budget.
func TestConcurrentReserveNeverExceedsBudget(t *testing.T) {
	l := New(10, time.Minute, WithMaxKeys(64))
	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			if r, ok := l.Reserve("shared"); ok {
				granted.Add(1)
				_ = r
			}
			l.Fail(fmt.Sprint(i % 40))
			l.Blocked(fmt.Sprint(i % 7))
			l.RetryAfter("shared")
		})
	}
	wg.Wait()
	if g := granted.Load(); g != 10 {
		t.Errorf("granted %d reservations, want exactly 10", g)
	}
	if l.Len() > 64 {
		t.Errorf("Len = %d exceeds the cap", l.Len())
	}
}

func TestMinimumBudget(t *testing.T) {
	l := New(0, time.Minute)
	if _, ok := l.Reserve("k"); !ok {
		t.Error("a budget below 1 is treated as 1")
	}
	if l.Window() != time.Minute {
		t.Error("Window")
	}
}

func TestKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"192.0.2.7", "192.0.2.7"},
		{"::ffff:192.0.2.7", "192.0.2.7"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"sock", "sock"},
		{"", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := Key(tc.in); got != tc.want {
				t.Errorf("Key(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
