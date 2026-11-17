// Package extract pulls one-time codes out of message text. It is pure: no
// database, no clock, so it is covered by table driven tests.
package extract

import (
	"errors"
	"regexp"
	"strings"
)

// Match kinds, how a code was found.
const (
	// KindKeyword is a code next to a word like "code", "OTP" or "PIN".
	KindKeyword = "keyword"
	// KindDigits is a standalone run of 6 digits, used when no keyword matched.
	KindDigits = "digits"
	// KindCustom is a match of a caller supplied pattern.
	KindCustom = "custom"
)

const (
	// MaxPatternLength bounds a caller supplied pattern in bytes.
	MaxPatternLength = 200
	// MaxCodes bounds the number of codes returned.
	MaxCodes = 20
	// maxScan bounds the text that is scanned, in bytes.
	maxScan = 256 << 10
)

// ErrBadPattern is returned for a pattern that is too long or not valid RE2.
var ErrBadPattern = errors.New("invalid code pattern")

// Code is one extracted one-time code.
type Code struct {
	// Value is the code without separators: "123 456" becomes "123456".
	Value string `json:"value"`
	// Kind says how it was found: keyword, digits or custom.
	Kind string `json:"kind"`
}

var (
	urlRe = regexp.MustCompile(`(?i)\b(?:https?|ftp)://\S+|\bmailto:\S+`)
	// A keyword, up to 40 characters of glue on the same line ("is", ":",
	// "below"), then the code itself: 4 to 8 digits, two groups of 3 or 4
	// digits split by a space or dash, or an alphanumeric token holding a digit.
	keywordRe = regexp.MustCompile(`(?i)\b(?:code|otp|pin|passcode|password|token)\b[^\n\d]{0,40}?` +
		`(\d{3,4}[ -]\d{3,4}|\d{4,8}|[A-Z0-9]{3,5}-[A-Z0-9]{3,5}|[A-Z0-9]{6,10})\b`)
	// digitRunRe finds runs of ASCII digits. A run is a standalone code when
	// it has exactly 6 digits and its neighbours pass digitBoundaryOK. The
	// boundaries are checked in code, not consumed by the pattern, so codes
	// separated by a single character are all found (RE2 has no lookaround).
	digitRunRe  = regexp.MustCompile(`\d+`)
	sepStripper = strings.NewReplacer(" ", "", "-", "")
)

const (
	standaloneDigits = 6
	// Characters that make a digit run part of a number, date, time or phone
	// number rather than a code. A leading '+' marks a phone number, a
	// trailing one does not.
	notBefore = ".,/:+-"
	notAfter  = ".,/:-"
	// firstBatch is the number of matches requested first; it grows
	// geometrically only while duplicates or empty matches keep the result
	// under MaxCodes, up to maxMatches, past which the scan stops.
	firstBatch = MaxCodes * 4
	maxMatches = firstBatch * 64
)

// Codes returns the one-time codes found in text, distinct, in order of
// appearance. With a custom pattern (RE2 syntax, group 1 is the code when it
// has a group, otherwise the whole match) only that pattern is used.
// Without one: codes next to a keyword; when there are none, standalone
// 6 digit numbers. URLs are ignored so ids in links are never reported.
func Codes(text, pattern string) ([]Code, error) {
	if len(text) > maxScan {
		text = text[:maxScan]
	}
	if pattern != "" {
		if len(pattern) > MaxPatternLength {
			return nil, ErrBadPattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, ErrBadPattern
		}
		return collectBounded(submatches(re, text), KindCustom, true, false), nil
	}
	text = urlRe.ReplaceAllString(text, " ")
	got := collectBounded(submatches(keywordRe, text), KindKeyword, false, true)
	if len(got) > 0 {
		return got, nil
	}
	return collectBounded(func(n int) ([][]string, bool) { return standaloneDigitRuns(text, n) },
		KindDigits, false, false), nil
}

// finder returns at most n matches and whether more may exist past them.
type finder func(n int) (matches [][]string, more bool)

func submatches(re *regexp.Regexp, text string) finder {
	return func(n int) ([][]string, bool) {
		ms := re.FindAllStringSubmatch(text, n)
		return ms, len(ms) == n
	}
}

// standaloneDigitRuns returns, as single element matches, the 6 digit runs of
// text whose neighbours are not number separators, scanning at most n runs.
func standaloneDigitRuns(text string, n int) ([][]string, bool) {
	locs := digitRunRe.FindAllStringIndex(text, n)
	out := make([][]string, 0, len(locs))
	for _, loc := range locs {
		if loc[1]-loc[0] != standaloneDigits || !digitBoundaryOK(text, loc[0], loc[1]) {
			continue
		}
		out = append(out, []string{text[loc[0]:loc[1]]})
	}
	return out, len(locs) == n
}

func digitBoundaryOK(text string, start, end int) bool {
	if start > 0 && strings.IndexByte(notBefore, text[start-1]) >= 0 {
		return false
	}
	return end >= len(text) || strings.IndexByte(notAfter, text[end]) < 0
}

// collectBounded asks find for at most n matches and grows n only when the
// distinct codes found so far are below MaxCodes and more matches may exist,
// so a pattern matching everywhere never materialises the whole scan window.
func collectBounded(find finder, kind string, custom, requireDigit bool) []Code {
	for n := firstBatch; ; n *= 4 {
		matches, more := find(n)
		out := collect(matches, kind, custom, requireDigit)
		if len(out) >= MaxCodes || !more || n >= maxMatches {
			return out
		}
	}
}

// collect turns regexp matches into distinct codes. The code is capture
// group 1 when the pattern has a group, the whole match otherwise.
func collect(matches [][]string, kind string, custom, requireDigit bool) []Code {
	seen := map[string]struct{}{}
	out := []Code{}
	for _, m := range matches {
		v := m[0]
		if len(m) > 1 {
			v = m[1]
		}
		if !custom {
			v = sepStripper.Replace(v)
		}
		v = strings.TrimSpace(v)
		if v == "" || (requireDigit && !strings.ContainsAny(v, "0123456789")) {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, Code{Value: v, Kind: kind})
		if len(out) == MaxCodes {
			break
		}
	}
	return out
}
