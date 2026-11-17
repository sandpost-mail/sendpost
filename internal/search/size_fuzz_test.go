package search

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func FuzzSizeToBytes(f *testing.F) {
	for _, s := range []string{"2kb", "4MB", "1.5m", "10", "1 kb", "99999999999999999999", "9999999999999999kb", "x", "-1", "1.", "0.5k"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := sizeToBytes(s)
		if err != nil {
			if !errors.Is(err, ErrSizeTooLarge) || n != 0 {
				t.Fatalf("%q: %d, %v", s, n, err)
			}
			return
		}
		if n > math.MaxInt64 {
			t.Fatalf("%q: %d does not fit int64", s, n)
		}
		// Plain integers small enough to be exact as a float64 are exact.
		if v, perr := strconv.ParseUint(s, 10, 64); perr == nil && v < 1<<53 && n != v {
			t.Fatalf("%q: got %d want %d", s, n, v)
		}
	})
}

func BenchmarkSizeToBytes(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sizeToBytes("1.5mb"); err != nil {
			b.Fatal(err)
		}
	}
}
