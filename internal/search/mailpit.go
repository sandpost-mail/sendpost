package search

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filter is a search in the syntax of Mailpit (see docs/MAILPIT_COMPATIBILITY.md):
// a list of terms that must all hold. The terms are stored as parallel
// slices, the shape the sp_filter_match SQL function takes. Kinds[i] names the
// field, Vals[i] is an ILIKE pattern, a tag name, a timestamp or a byte count
// depending on the kind, and Negs[i] inverts the term.
//
// The zero value matches every message.
type Filter struct {
	Kinds []string
	Vals  []string
	Negs  []bool
}

// IsZero reports whether the filter has no term and so matches everything.
func (f Filter) IsZero() bool { return len(f.Kinds) == 0 }

// Len is the number of terms.
func (f Filter) Len() int { return len(f.Kinds) }

func (f Filter) with(kind, val string, neg bool) Filter {
	return Filter{
		Kinds: append(slices.Clone(f.Kinds), kind),
		Vals:  append(slices.Clone(f.Vals), val),
		Negs:  append(slices.Clone(f.Negs), neg),
	}
}

// kindText is the kind of a free text term.
const kindText = "text"

// addressKeys are the "key:" prefixes matched as a substring of an address.
var addressKeys = map[string]string{
	"to:": "to", "from:": "from", "cc:": "cc", "bcc:": "bcc", "reply-to:": "reply-to",
	"addressed:": "addressed", "subject:": "subject", "message-id:": "message-id",
}

// flagTerms are the is: and has: terms. The bool is the negation they imply.
var flagTerms = map[string]struct {
	kind string
	neg  bool
}{
	"is:read": {"is-read", false}, "is:unread": {"is-read", true}, "is:tagged": {"is-tagged", false},
	"has:attachment": {"has-attachment", false}, "has:attachments": {"has-attachment", false},
	"has:inline": {"has-inline", false}, "has:inlines": {"has-inline", false},
}

// LoadLocation returns the zone named by a Mailpit tz parameter. Mailpit
// ignores an unknown zone and uses the server's local one, Sandpost uses UTC.
func LoadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ParseMailpit parses s. Dates without a zone are read in loc. Unlike Mailpit,
// an invalid date is an error rather than a term that is silently dropped,
// because a dropped term would widen a delete.
func ParseMailpit(s string, loc *time.Location) (Filter, error) {
	if len(s) > MaxQueryLength {
		return Filter{}, ErrQueryTooLong
	}
	var f Filter
	for _, arg := range splitArgs(s) {
		neg := false
		if len(arg) > 1 && (arg[0] == '-' || arg[0] == '!') {
			neg, arg = true, arg[1:]
		}
		var err error
		if f, err = addTerm(f, arg, neg, loc); err != nil {
			return Filter{}, err
		}
	}
	return f, nil
}

// addTerm turns one argument into a term. Arguments that carry no value are
// dropped, as Mailpit does.
func addTerm(f Filter, arg string, neg bool, loc *time.Location) (Filter, error) {
	lw := strings.ToLower(arg)
	if flag, ok := flagTerms[lw]; ok {
		return f.with(flag.kind, "", neg != flag.neg), nil
	}
	for prefix, kind := range addressKeys {
		if rest, ok := strings.CutPrefix(lw, prefix); ok {
			val := strings.TrimSpace(arg[len(prefix):])
			if strings.TrimSpace(rest) == "" {
				return f, nil
			}
			return f.with(kind, LikePattern(val), neg), nil
		}
	}
	switch {
	case strings.HasPrefix(lw, "tag:"):
		if val := strings.TrimSpace(arg[4:]); val != "" {
			return f.with("tag", val, neg), nil
		}
		return f, nil
	case strings.HasPrefix(lw, "after:"), strings.HasPrefix(lw, "before:"):
		return addDate(f, lw[:strings.IndexByte(lw, ':')], strings.TrimSpace(arg[strings.IndexByte(arg, ':')+1:]), neg, loc)
	case strings.HasPrefix(lw, "larger:"), strings.HasPrefix(lw, "smaller:"):
		key, val, _ := strings.Cut(arg, ":")
		if n := sizeToBytes(strings.TrimSpace(val)); n > 0 {
			return f.with(strings.ToLower(key), strconv.FormatUint(n, 10), neg), nil
		}
	}
	// A key with an empty value is dropped, everything else is free text.
	if isKey(lw) {
		return f, nil
	}
	return f.with(kindText, LikePattern(strings.TrimSpace(arg)), neg), nil
}

// isKey reports whether k ("name:") is an operator that takes a value.
func isKey(k string) bool {
	if _, ok := addressKeys[k]; ok {
		return true
	}
	switch k {
	case "tag:", "after:", "before:", "larger:", "smaller:":
		return true
	}
	return false
}

func addDate(f Filter, key, val string, neg bool, loc *time.Location) (Filter, error) {
	if val == "" {
		return f, nil
	}
	t, err := parseDate(val, loc)
	if err != nil {
		return Filter{}, fmt.Errorf("%s:%s: %w", key, val, ErrInvalidDate)
	}
	return f.with(key, t.UTC().Format(time.RFC3339), neg), nil
}

var dateLayouts = []string{
	"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02T15:04:05",
}

func parseDate(v string, loc *time.Location) (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, ErrInvalidDate
}

// splitArgs splits s on spaces, keeping double quoted runs together and
// dropping the quotes, like Mailpit's argument parser.
func splitArgs(s string) []string {
	var args []string
	var sb strings.Builder
	quoted := false
	flush := func() {
		if v := strings.TrimSpace(sb.String()); v != "" {
			args = append(args, v)
		}
		sb.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			sb.WriteRune(r)
		}
	}
	flush()
	return args
}

var sizeRe = regexp.MustCompile(`^(\d+)(\.\d+)?\s?([a-z]{1,2})?$`)

// sizeToBytes reads sizes such as 2kb, 4MB or 1.5m. It returns 0 for text that
// is not a size.
func sizeToBytes(v string) uint64 {
	m := sizeRe.FindStringSubmatch(strings.ToLower(v))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1]+m[2], 64)
	if err != nil || n < 0 {
		return 0
	}
	switch m[3] {
	case "":
		return uint64(n)
	case "k", "kb":
		return uint64(n * 1024)
	case "m", "mb":
		return uint64(n * 1024 * 1024)
	}
	return 0
}
