package search_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/search"
)

// likeKinds are the Mailpit kinds whose value is an ILIKE pattern.
var likeKinds = map[string]bool{
	"text": true, "from": true, "to": true, "cc": true, "bcc": true, "reply-to": true,
	"addressed": true, "subject": true, "message-id": true,
}

// flagKinds are the Mailpit kinds that carry no value.
var flagKinds = map[string]bool{"is-read": true, "is-tagged": true, "has-attachment": true, "has-inline": true}

var mailpitSeeds = []string{
	`-from:"a b" is:unread larger:2kb`,
	`to:bob@example.test subject:"order shipped" !has:attachment`,
	`tag:"run 1" after:2026-10-01 before:"2026-10-14 13:30"`,
	`smaller:1.5m message-id:<x@y> reply-to:a addressed:b cc: bcc:c`,
	`100% _under\score "unterminated`,
	`larger:99999999999999999999 smaller:9999999999999999kb`,
	`after:never`,
	`-- ! - is:read is:tagged has:inlines`,
}

// checkLikePattern fails unless p is "%" + escaped text + "%" with no
// unescaped LIKE metacharacter and at least one character of text.
func checkLikePattern(t *testing.T, p string) {
	t.Helper()
	inner, ok := strings.CutPrefix(p, "%")
	if ok {
		inner, ok = strings.CutSuffix(inner, "%")
	}
	if !ok || inner == "" {
		t.Fatalf("pattern %q is not %%text%%", p)
	}
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '\\':
			if i+1 == len(inner) || !strings.ContainsRune(`\%_`, rune(inner[i+1])) {
				t.Fatalf("pattern %q has a dangling escape at %d", p, i)
			}
			i++
		case '%', '_':
			t.Fatalf("pattern %q has an unescaped %q at %d", p, inner[i], i)
		}
	}
}

func checkFilter(t *testing.T, f search.Filter) {
	t.Helper()
	if len(f.Kinds) != len(f.Vals) || len(f.Vals) != len(f.Negs) {
		t.Fatalf("mismatched slices: %+v", f)
	}
	if f.Len() > search.MaxTerms {
		t.Fatalf("%d terms, max %d", f.Len(), search.MaxTerms)
	}
	for i, kind := range f.Kinds {
		val := f.Vals[i]
		switch {
		case likeKinds[kind]:
			checkLikePattern(t, val)
		case flagKinds[kind]:
			if val != "" {
				t.Fatalf("flag %s has value %q", kind, val)
			}
		case kind == "tag":
			if val == "" || val != strings.TrimSpace(val) {
				t.Fatalf("tag value %q", val)
			}
		case kind == "after", kind == "before":
			if _, err := time.Parse(time.RFC3339, val); err != nil {
				t.Fatalf("%s value %q: %v", kind, val, err)
			}
		case kind == "larger", kind == "smaller":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n <= 0 {
				t.Fatalf("%s value %q: %v", kind, val, err)
			}
		default:
			t.Fatalf("unknown kind %q", kind)
		}
	}
}

func FuzzParseMailpit(f *testing.F) {
	for _, s := range mailpitSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		flt, err := search.ParseMailpit(s, time.UTC)
		if err != nil {
			if !errors.Is(err, search.ErrQueryTooLong) && !errors.Is(err, search.ErrTooManyTerms) &&
				!errors.Is(err, search.ErrInvalidDate) && !errors.Is(err, search.ErrSizeTooLarge) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			if !flt.IsZero() {
				t.Fatalf("filter %+v next to error %v", flt, err)
			}
			return
		}
		checkFilter(t, flt)
		again, err := search.ParseMailpit(s, time.UTC)
		if err != nil || !reflect.DeepEqual(flt, again) {
			t.Fatalf("not deterministic: %+v / %+v, %v", flt, again, err)
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`from:alice to:bob@example.test subject:"order shipped" tag:run-42`,
		`body:invoice date:2026-10-14 after:2026-10-01 before:2026-10-15T10:00:00Z`,
		`"free phrase" word https://x.test/a:b unknown:key`,
		`date:nope`, `"unterminated quote`, "tab\tand\nnewline",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := search.Parse(s)
		if err != nil {
			if !errors.Is(err, search.ErrQueryTooLong) && !errors.Is(err, search.ErrInvalidDate) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			if !q.IsZero() {
				t.Fatalf("query %+v next to error %v", q, err)
			}
			return
		}
		for name, v := range map[string]string{"from": q.From, "to": q.To, "subject": q.Subject, "tag": q.Tag, "body": q.Body} {
			if v != strings.TrimSpace(v) {
				t.Fatalf("%s value %q is not trimmed", name, v)
			}
		}
		for _, tm := range []*time.Time{q.After, q.Before} {
			if tm != nil && tm.Location() != time.UTC {
				t.Fatalf("time %v is not UTC", tm)
			}
		}
		if strings.TrimSpace(s) == "" && !q.IsZero() {
			t.Fatalf("blank input gave %+v", q)
		}
		again, err := search.Parse(s)
		if err != nil || !reflect.DeepEqual(q, again) {
			t.Fatalf("not deterministic: %+v / %+v, %v", q, again, err)
		}
	})
}

func FuzzLikePattern(f *testing.F) {
	for _, s := range []string{"a", `100%`, `a_b`, `back\slash`, `\%_`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := search.LikePattern(s)
		if s == "" {
			if p != "" {
				t.Fatalf("empty text gave %q", p)
			}
			return
		}
		checkLikePattern(t, p)
		inner := p[1 : len(p)-1]
		unescaped := strings.NewReplacer(`\\`, `\`, `\%`, `%`, `\_`, `_`).Replace(inner)
		if unescaped != s {
			t.Fatalf("round trip %q -> %q -> %q", s, p, unescaped)
		}
	})
}

// The parse benchmarks guard the tokenizers against accidental quadratic
// behaviour; queries are bounded by MaxQueryLength.
func BenchmarkParseMailpit(b *testing.B) {
	q := `-from:"a b" to:bob@example.test is:unread larger:2kb tag:"run 1" after:2026-10-01 subject:invoice words here`
	b.ReportAllocs()
	for b.Loop() {
		if _, err := search.ParseMailpit(q, time.UTC); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseMailpitMaxLength(b *testing.B) {
	q := strings.Repeat(`"`, search.MaxQueryLength/2) + strings.Repeat("a", search.MaxQueryLength/2)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = search.ParseMailpit(q, time.UTC)
	}
}

func BenchmarkParse(b *testing.B) {
	q := `from:alice to:bob@example.test subject:"order shipped" tag:run-42 body:invoice after:2026-10-01 free words`
	b.ReportAllocs()
	for b.Loop() {
		if _, err := search.Parse(q); err != nil {
			b.Fatal(err)
		}
	}
}
