package ingest

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const (
	maxLinks = 500
	// urlScanFactor bounds the bare URLs scanned per remaining link slot.
	urlScanFactor = 8
)

var bareURL = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'\x60]+`)

// ExtractLinks returns the distinct http, https and mailto links found in the
// HTML anchors first, then bare URLs in the text, in order of appearance.
func ExtractLinks(htmlBody, text string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(clean(u))
		if u == "" || len(out) >= maxLinks {
			return
		}
		if _, dup := seen[u]; dup {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	eachAnchorHref(htmlBody, func(u string) bool {
		if linkScheme(u) {
			add(u)
		}
		return len(out) < maxLinks
	})
	if len(out) >= maxLinks {
		return out
	}
	// Duplicates are common, so scan a bounded multiple of the remaining
	// budget instead of every URL of a possibly huge text body.
	for _, m := range bareURL.FindAllString(text, (maxLinks-len(out))*urlScanFactor) {
		add(trimURLPunctuation(m))
		if len(out) >= maxLinks {
			break
		}
	}
	return out
}

func linkScheme(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:")
}

// eachAnchorHref calls yield with the href of every a and area tag of body, in
// order, until yield returns false. It reads tag names and attributes without
// building tokens, so tags that are not anchors cost no allocation.
func eachAnchorHref(body string, yield func(href string) bool) {
	if body == "" {
		return
	}
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if !hasAttr || (string(name) != "a" && string(name) != "area") {
				continue
			}
			for more := true; more; {
				var key, val []byte
				key, val, more = z.TagAttr()
				if string(key) == "href" && !yield(string(val)) {
					return
				}
			}
		}
	}
}

// trimURLPunctuation drops sentence punctuation that is almost never part of
// the URL, and an unbalanced closing parenthesis.
func trimURLPunctuation(u string) string {
	for {
		trimmed := strings.TrimRight(u, ".,;:!?")
		if strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, ")") > strings.Count(trimmed, "(") {
			trimmed = strings.TrimSuffix(trimmed, ")")
		}
		if trimmed == u {
			return u
		}
		u = trimmed
	}
}
