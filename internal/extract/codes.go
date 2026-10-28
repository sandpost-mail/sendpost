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
	digitsRe = regexp.MustCompile(`(?:^|[^\d.,/:+-])(\d{6})(?:$|[^\d.,/:-])`)
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
		return collect(re.FindAllStringSubmatch(text, -1), KindCustom, true, false), nil
	}
	text = urlRe.ReplaceAllString(text, " ")
	if got := collect(keywordRe.FindAllStringSubmatch(text, -1), KindKeyword, false, true); len(got) > 0 {
		return got, nil
	}
	return collect(digitsRe.FindAllStringSubmatch(text, -1), KindDigits, false, false), nil
}

// collect turns regexp matches into distinct codes. The code is the last
// capture group when the pattern has one (the whole match otherwise).
func collect(matches [][]string, kind string, custom, requireDigit bool) []Code {
	seen := map[string]struct{}{}
	out := []Code{}
	for _, m := range matches {
		v := m[len(m)-1]
		if custom && len(m) == 1 {
			v = m[0]
		}
		if !custom {
			v = strings.NewReplacer(" ", "", "-", "").Replace(v)
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
