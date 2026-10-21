package search_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/search"
)

func TestParseMailpit(t *testing.T) {
	utc := time.UTC
	tests := []struct {
		name  string
		query string
		kinds []string
		vals  []string
		negs  []bool
	}{
		{"empty", "", nil, nil, nil},
		{"blank", "   ", nil, nil, nil},
		{"from", "from:alice", []string{"from"}, []string{"%alice%"}, []bool{false}},
		{"case of the key is ignored", "FROM:Alice", []string{"from"}, []string{"%Alice%"}, []bool{false}},
		{"to cc bcc reply-to", "to:a cc:b bcc:c reply-to:d",
			[]string{"to", "cc", "bcc", "reply-to"}, []string{"%a%", "%b%", "%c%", "%d%"}, []bool{false, false, false, false}},
		{"addressed", "addressed:x@y.test", []string{"addressed"}, []string{"%x@y.test%"}, []bool{false}},
		{"quoted value", `subject:"order shipped"`, []string{"subject"}, []string{"%order shipped%"}, []bool{false}},
		{"minus negates", "-subject:spam", []string{"subject"}, []string{"%spam%"}, []bool{true}},
		{"bang negates", "!from:bot", []string{"from"}, []string{"%bot%"}, []bool{true}},
		{"message-id", "message-id:abc@x", []string{"message-id"}, []string{"%abc@x%"}, []bool{false}},
		{"tag is exact", "tag:run-42", []string{"tag"}, []string{"run-42"}, []bool{false}},
		{"is read", "is:read", []string{"is-read"}, []string{""}, []bool{false}},
		{"is unread is negated read", "is:unread", []string{"is-read"}, []string{""}, []bool{true}},
		{"not unread", "-is:unread", []string{"is-read"}, []string{""}, []bool{false}},
		{"is tagged", "is:tagged", []string{"is-tagged"}, []string{""}, []bool{false}},
		{"has attachment", "has:attachment", []string{"has-attachment"}, []string{""}, []bool{false}},
		{"has attachments", "has:attachments", []string{"has-attachment"}, []string{""}, []bool{false}},
		{"has inline", "has:inline", []string{"has-inline"}, []string{""}, []bool{false}},
		{"after date", "after:2026-10-14", []string{"after"}, []string{"2026-10-14T00:00:00Z"}, []bool{false}},
		{"before with time", `before:"2026-10-14 13:30"`, []string{"before"}, []string{"2026-10-14T13:30:00Z"}, []bool{false}},
		{"negated after is before", "-after:2026-10-14", []string{"after"}, []string{"2026-10-14T00:00:00Z"}, []bool{true}},
		{"larger kb", "larger:2kb", []string{"larger"}, []string{"2048"}, []bool{false}},
		{"smaller mb", "smaller:1.5m", []string{"smaller"}, []string{"1572864"}, []bool{false}},
		{"bad size is free text", "larger:abc", []string{"text"}, []string{"%larger:abc%"}, []bool{false}},
		{"free text", "hello world", []string{"text", "text"}, []string{"%hello%", "%world%"}, []bool{false, false}},
		{"quoted free text", `"hello world"`, []string{"text"}, []string{"%hello world%"}, []bool{false}},
		{"negated free text", "-spam", []string{"text"}, []string{"%spam%"}, []bool{true}},
		{"escapes like metacharacters", "100%_done", []string{"text"}, []string{`%100\%\_done%`}, []bool{false}},
		{"unknown key is free text", "foo:bar", []string{"text"}, []string{"%foo:bar%"}, []bool{false}},
		{"empty value is dropped", "from: subject:x", []string{"subject"}, []string{"%x%"}, []bool{false}},
		{"repeated key keeps both terms", "to:a to:b", []string{"to", "to"}, []string{"%a%", "%b%"}, []bool{false, false}},
		{"lone dash is free text", "- x", []string{"text", "text"}, []string{"%-%", "%x%"}, []bool{false, false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := search.ParseMailpit(tc.query, utc)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.Kinds, tc.kinds) || !reflect.DeepEqual(f.Vals, tc.vals) || !reflect.DeepEqual(f.Negs, tc.negs) {
				t.Errorf("got kinds=%v vals=%q negs=%v\nwant kinds=%v vals=%q negs=%v", f.Kinds, f.Vals, f.Negs, tc.kinds, tc.vals, tc.negs)
			}
			if f.IsZero() != (len(tc.kinds) == 0) {
				t.Errorf("IsZero = %v", f.IsZero())
			}
		})
	}
}

func TestParseMailpitTimezone(t *testing.T) {
	loc := search.LoadLocation("Pacific/Auckland")
	f, err := search.ParseMailpit("after:2026-01-15", loc)
	if err != nil {
		t.Fatal(err)
	}
	// Auckland is UTC+13 in January.
	if want := "2026-01-14T11:00:00Z"; f.Vals[0] != want {
		t.Errorf("got %q want %q", f.Vals[0], want)
	}
	if got := search.LoadLocation("Not/AZone"); got != time.UTC {
		t.Errorf("an unknown zone must fall back to UTC, got %v", got)
	}
	if got := search.LoadLocation(""); got != time.UTC {
		t.Errorf("empty zone must be UTC, got %v", got)
	}
}

func TestParseMailpitErrors(t *testing.T) {
	tests := []struct {
		name, query string
		want        error
	}{
		{"invalid after", "after:yesterday-ish", search.ErrInvalidDate},
		{"invalid before", "before:2026-13-45", search.ErrInvalidDate},
		{"too long", strings.Repeat("a ", search.MaxQueryLength), search.ErrQueryTooLong},
		{"too many terms", strings.Repeat("a ", search.MaxTerms+1), search.ErrTooManyTerms},
		{"larger overflows bigint", "larger:99999999999999999999", search.ErrSizeTooLarge},
		{"smaller overflows after kb", "smaller:9007199254740992kb", search.ErrSizeTooLarge},
		{"smaller overflows after mb", "-smaller:8796093022208mb", search.ErrSizeTooLarge},
		{"huge digits", "larger:" + strings.Repeat("9", 400), search.ErrSizeTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := search.ParseMailpit(tc.query, time.UTC); !errors.Is(err, tc.want) {
				t.Errorf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestFilterLenMatchesParallelArrays(t *testing.T) {
	f, err := search.ParseMailpit("from:a -to:b hello", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if f.Len() != 3 || len(f.Vals) != 3 || len(f.Negs) != 3 {
		t.Errorf("arrays out of step: %+v", f)
	}
}

func TestTermCapAndSizeLimits(t *testing.T) {
	if _, err := search.ParseMailpit(strings.Repeat("a ", search.MaxTerms), time.UTC); err != nil {
		t.Errorf("exactly the cap must pass: %v", err)
	}
	// A value that fits after the unit multiplication is accepted.
	f, err := search.ParseMailpit("larger:8388607mb", time.UTC)
	if err != nil || f.Vals[0] != "8796091973632" {
		t.Errorf("%v %v", f, err)
	}
	if _, err := search.ParseMailpit("larger:9223372036854775807", time.UTC); err != nil && !errors.Is(err, search.ErrSizeTooLarge) {
		t.Errorf("max int64 must either parse or be rejected cleanly: %v", err)
	}
}
