package delivery

import (
	"testing"
	"time"
)

func TestWindowLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newWindowLimiter(2, time.Minute, func() time.Time { return now })

	for i := range 2 {
		if _, ok := l.take(1); !ok {
			t.Fatalf("event %d refused", i)
		}
	}
	now = now.Add(20 * time.Second)
	wait, ok := l.take(1)
	if ok || wait != 40*time.Second {
		t.Fatalf("third event: ok %v wait %v", ok, wait)
	}
	if _, ok := l.take(2); !ok {
		t.Error("another key has its own budget")
	}
	now = now.Add(41 * time.Second)
	if _, ok := l.take(1); !ok {
		t.Error("the budget must come back after the window")
	}
}
