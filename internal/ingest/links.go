package ingest

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const maxLinks = 500

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
	for _, u := range anchorHrefs(htmlBody) {
		if linkScheme(u) {
			add(u)
		}
	}
	for _, m := range bareURL.FindAllString(text, -1) {
		add(trimURLPunctuation(m))
	}
	return out
}

func linkScheme(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:")
}

func anchorHrefs(body string) []string {
	if body == "" {
		return nil
	}
	var out []string
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != "a" && tok.Data != "area" {
				continue
			}
			for _, a := range tok.Attr {
				if a.Key == "href" {
					out = append(out, a.Val)
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
