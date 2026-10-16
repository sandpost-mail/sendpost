// Package search parses the message search syntax.
//
// A query is a list of whitespace separated terms. A term of the form key:value
// filters on a field, anything else is free text matched against the subject and
// the body. Values with spaces are double quoted: subject:"order shipped".
//
//	from:alice            sender address or name contains alice
//	to:bob@example.test   any recipient contains the text
//	subject:welcome       subject contains the text
//	tag:run-42            message has exactly this tag
//	body:invoice          full text search in the body
//	date:2026-10-14       received that day (UTC)
//	after:2026-10-01      received at or after that day
//	before:2026-10-15     received before that day
//
// A repeated key keeps its last value. Unknown keys are treated as free text,
// so URLs and addresses typed as plain words still work.
package search

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxQueryLength bounds the accepted query size in bytes.
const MaxQueryLength = 500

// ErrInvalidDate is returned for a date term that is not YYYY-MM-DD or RFC 3339.
var ErrInvalidDate = errors.New("invalid date")

// ErrQueryTooLong is returned when the query exceeds MaxQueryLength.
var ErrQueryTooLong = fmt.Errorf("query is longer than %d bytes", MaxQueryLength)

// Query is a parsed search. The zero value matches everything.
type Query struct {
	From    string
	To      string
	Subject string
	Tag     string
	Body    string
	// Free is the free text, words and "quoted phrases", in websearch syntax.
	Free   string
	After  *time.Time
	Before *time.Time
}

// IsZero reports whether q has no constraints.
func (q Query) IsZero() bool {
	return q.From == "" && q.To == "" && q.Subject == "" && q.Tag == "" && q.Body == "" &&
		q.Free == "" && q.After == nil && q.Before == nil
}

// Parse parses s. It is total apart from date and length errors.
func Parse(s string) (Query, error) {
	if len(s) > MaxQueryLength {
		return Query{}, ErrQueryTooLong
	}
	var q Query
	var free []string
	for _, tok := range tokenize(s) {
		key, val, ok := splitTerm(tok)
		if !ok {
			free = append(free, tok.freeText())
			continue
		}
		switch key {
		case "from":
			q.From = val
		case "to":
			q.To = val
		case "subject":
			q.Subject = val
		case "tag":
			q.Tag = val
		case "body":
			q.Body = val
		case "date":
			day, err := parseTime(val)
			if err != nil {
				return Query{}, fmt.Errorf("date:%s: %w", val, err)
			}
			next := day.AddDate(0, 0, 1)
			q.After, q.Before = laterOf(q.After, &day), earlierOf(q.Before, &next)
		case "after":
			t, err := parseTime(val)
			if err != nil {
				return Query{}, fmt.Errorf("after:%s: %w", val, err)
			}
			q.After = laterOf(q.After, &t)
		case "before":
			t, err := parseTime(val)
			if err != nil {
				return Query{}, fmt.Errorf("before:%s: %w", val, err)
			}
			q.Before = earlierOf(q.Before, &t)
		}
	}
	q.Free = strings.Join(free, " ")
	return q, nil
}

type token struct {
	text   string
	quoted bool // the value (or whole token) was quoted
}

func (t token) freeText() string {
	if t.quoted && strings.ContainsAny(t.text, " \t") {
		return `"` + t.text + `"`
	}
	return t.text
}

// tokenize splits on whitespace, keeping double quoted runs together. A quote
// directly after "key:" belongs to the value. An unterminated quote runs to the end.
func tokenize(s string) []token {
	var out []token
	var cur strings.Builder
	inQuote, quoted := false, false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, token{text: cur.String(), quoted: quoted})
		}
		cur.Reset()
		quoted = false
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			quoted = true
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

var knownKeys = map[string]bool{
	"from": true, "to": true, "subject": true, "tag": true, "body": true,
	"date": true, "after": true, "before": true,
}

func splitTerm(t token) (key, val string, ok bool) {
	k, v, found := strings.Cut(t.text, ":")
	k = strings.ToLower(k)
	if !found || !knownKeys[k] || strings.TrimSpace(v) == "" {
		return "", "", false
	}
	return k, strings.TrimSpace(v), true
}

func parseTime(v string) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", v, time.UTC); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, ErrInvalidDate
}

func laterOf(a, b *time.Time) *time.Time {
	if a == nil || b.After(*a) {
		t := *b
		return &t
	}
	return a
}

func earlierOf(a, b *time.Time) *time.Time {
	if a == nil || b.Before(*a) {
		t := *b
		return &t
	}
	return a
}

// LikePattern turns text into an ILIKE pattern matching it as a substring, with
// LIKE metacharacters escaped. Empty text yields "".
func LikePattern(text string) string {
	if text == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}
