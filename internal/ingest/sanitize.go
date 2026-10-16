package ingest

import "strings"

const replacementRune = "�"

// clean makes s safe for a Postgres text or jsonb column: invalid UTF-8 is
// replaced and NUL bytes, which Postgres rejects, are dropped.
func clean(s string) string {
	s = strings.ToValidUTF8(s, replacementRune)
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return s
}

// truncateRunes returns at most n runes of s.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
