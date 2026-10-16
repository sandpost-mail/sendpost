package ingest

import (
	"sort"
	"strings"
)

// Tag rule kinds.
const (
	RuleHeader    = "header"
	RuleRecipient = "recipient"
)

const (
	maxTagRunes      = 100
	valuePlaceholder = "{value}"
)

// Rule is a tag rule as stored per project.
type Rule struct {
	Kind       string
	HeaderName string
	Pattern    string
	Tag        string
}

// ApplyRules returns the sorted, distinct tags produced by rules for a
// message. Pure function: it never mutates its inputs.
//
// A header rule matches when the named header (case insensitive) has a value
// matching Pattern, an empty Pattern matches any value. A recipient rule matches
// when any recipient address matches Pattern. Patterns are case insensitive and
// '*' matches any run of characters. In a header rule, "{value}" in Tag is
// replaced by the matched header value.
func ApplyRules(rules []Rule, headers map[string][]string, recipients []Recipient) []string {
	set := map[string]struct{}{}
	add := func(tag string) {
		if tag = normalizeTag(tag); tag != "" {
			set[tag] = struct{}{}
		}
	}
	for _, r := range rules {
		switch r.Kind {
		case RuleHeader:
			for _, v := range headerValues(headers, r.HeaderName) {
				if wildcardMatch(r.Pattern, v) {
					add(strings.ReplaceAll(r.Tag, valuePlaceholder, strings.TrimSpace(v)))
				}
			}
		case RuleRecipient:
			for _, rc := range recipients {
				if wildcardMatch(r.Pattern, rc.Address) {
					add(r.Tag)
					break
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func headerValues(headers map[string][]string, name string) []string {
	var out []string
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			out = append(out, v...)
		}
	}
	return out
}

func normalizeTag(tag string) string {
	tag = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, clean(tag))
	return truncateRunes(strings.TrimSpace(tag), maxTagRunes)
}

// wildcardMatch reports whether s matches pattern, where '*' matches any run
// of characters. An empty pattern matches everything.
func wildcardMatch(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	p, str := strings.ToLower(pattern), strings.ToLower(s)
	parts := strings.Split(p, "*")
	if len(parts) == 1 {
		return p == str
	}
	if !strings.HasPrefix(str, parts[0]) {
		return false
	}
	str = str[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(str, mid)
		if i < 0 {
			return false
		}
		str = str[i+len(mid):]
	}
	return strings.HasSuffix(str, last)
}
