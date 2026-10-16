package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Query
	}{
		{"empty", "", Query{}},
		{"blanks", "   \t ", Query{}},
		{"free words", "welcome back", Query{Free: "welcome back"}},
		{"quoted phrase kept", `"order shipped" today`, Query{Free: `"order shipped" today`}},
		{"single quoted word loses quotes", `"hello"`, Query{Free: "hello"}},
		{"all fields", `from:alice to:bob@x.test subject:hi tag:run-1 body:invoice`,
			Query{From: "alice", To: "bob@x.test", Subject: "hi", Tag: "run-1", Body: "invoice"}},
		{"keys are case insensitive", "FROM:Alice Tag:X", Query{From: "Alice", Tag: "X"}},
		{"quoted value", `subject:"order shipped" plain`, Query{Subject: "order shipped", Free: "plain"}},
		{"last value wins", "from:a from:b", Query{From: "b"}},
		{"unknown key is free text", "http://x.test foo:bar", Query{Free: "http://x.test foo:bar"}},
		{"empty value is free text", "from: x", Query{Free: "from: x"}},
		{"date is a whole day", "date:2026-10-14", Query{After: day(2026, 10, 14), Before: day(2026, 10, 15)}},
		{"after and before", "after:2026-10-01 before:2026-10-15", Query{After: day(2026, 10, 1), Before: day(2026, 10, 15)}},
		{"date intersects with bounds", "date:2026-10-14 after:2026-10-14 before:2026-10-20",
			Query{After: day(2026, 10, 14), Before: day(2026, 10, 15)}},
		{"rfc3339", "after:2026-10-14T10:30:00+02:00", Query{After: ptr(time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC))}},
		{"unterminated quote runs to end", `subject:"half open`, Query{Subject: "half open"}},
		{"mixed", `from:alice welcome tag:x`, Query{From: "alice", Tag: "x", Free: "welcome"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"date:yesterday", "after:2026-13-01", "before:nope", "date:2026-10-1"} {
		if _, err := Parse(in); !errors.Is(err, ErrInvalidDate) {
			t.Errorf("%q: got %v", in, err)
		}
	}
	if _, err := Parse(strings.Repeat("a", MaxQueryLength+1)); !errors.Is(err, ErrQueryTooLong) {
		t.Errorf("long query: %v", err)
	}
	if _, err := Parse(strings.Repeat("a", MaxQueryLength)); err != nil {
		t.Errorf("limit is inclusive: %v", err)
	}
}

func TestIsZero(t *testing.T) {
	if !(Query{}).IsZero() {
		t.Error("zero")
	}
	for _, q := range []string{"x", "from:a", "tag:a", "after:2026-01-01"} {
		p, _ := Parse(q)
		if p.IsZero() {
			t.Errorf("%q should not be zero", q)
		}
	}
}

func TestLikePattern(t *testing.T) {
	tests := map[string]string{"": "", "abc": "%abc%", "50%_off": `%50\%\_off%`, `a\b`: `%a\\b%`}
	for in, want := range tests {
		if got := LikePattern(in); got != want {
			t.Errorf("LikePattern(%q)=%q want %q", in, got, want)
		}
	}
}
