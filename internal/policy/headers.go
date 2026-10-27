package policy

import (
	"bytes"
	"mime"
	"strings"
	"unicode/utf8"
)

// MaxSubjectPrefixRunes bounds the redirect subject prefix.
const MaxSubjectPrefixRunes = 100

// headerEdit says how to rewrite the header block of a message. Everything
// that is not named here is kept byte for byte, and the body is never touched.
type headerEdit struct {
	// subjectPrefix is the already validated prefix, empty for none.
	subjectPrefix string
	// originalTo and originalCc become X-Original-To and X-Original-Cc.
	originalTo, originalCc []string
}

// Headers always removed from outbound mail: Bcc must not reveal blind
// recipients, and X-Original-* are ours, a sender must not be able to forge
// them.
var strippedHeaders = map[string]bool{
	"bcc":           true,
	"x-original-to": true, "x-original-cc": true, "x-original-bcc": true,
}

// rewriteHeaders strips Bcc and any X-Original-* headers, optionally prefixes
// the subject and adds X-Original-To and X-Original-Cc. It returns a new slice.
func rewriteHeaders(raw []byte, e headerEdit) []byte {
	head, sep, body := splitMessage(raw)
	eol := lineEnding(head)
	fields := splitFields(head)

	var out bytes.Buffer
	out.Grow(len(raw) + 256)
	subjectDone := false
	for _, f := range fields {
		name, ok := fieldName(f)
		if ok && strippedHeaders[name] {
			continue
		}
		if ok && name == "subject" && !subjectDone && e.subjectPrefix != "" {
			subjectDone = true
			out.WriteString(prefixSubject(f, e.subjectPrefix))
			continue
		}
		out.WriteString(f)
	}
	if e.subjectPrefix != "" && !subjectDone {
		out.WriteString("Subject: " + encodePrefix(e.subjectPrefix) + eol)
	}
	writeOriginal(&out, "X-Original-To", e.originalTo, eol)
	writeOriginal(&out, "X-Original-Cc", e.originalCc, eol)
	if sep == "" {
		sep = eol // a message that is only headers still needs the blank line
	}
	out.WriteString(sep)
	out.Write(body)
	return out.Bytes()
}

// splitMessage splits at the first blank line. sep is the blank line itself
// ("\r\n" or "\n"), empty when the message has no body separator.
func splitMessage(raw []byte) (head []byte, sep string, body []byte) {
	for i := 0; i < len(raw); {
		j := bytes.IndexByte(raw[i:], '\n')
		if j < 0 {
			break
		}
		line := raw[i : i+j+1]
		if string(line) == "\n" || string(line) == "\r\n" {
			return raw[:i], string(line), raw[i+j+1:]
		}
		i += j + 1
	}
	return raw, "", nil
}

func lineEnding(head []byte) string {
	i := bytes.IndexByte(head, '\n')
	if i > 0 && head[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// splitFields cuts a header block into fields, each with its continuation
// lines and its line ending. A last line without a line ending gets one.
func splitFields(head []byte) []string {
	var fields []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(string(head), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "\n") {
			line += lineEnding(head)
		}
		if (line[0] == ' ' || line[0] == '\t') && cur.Len() > 0 {
			cur.WriteString(line)
			continue
		}
		if cur.Len() > 0 {
			fields = append(fields, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
}

// fieldName returns the lower case header name of a field, false for lines
// that are not "name: value" (they are kept as they are).
func fieldName(f string) (string, bool) {
	i := strings.IndexByte(f, ':')
	if i <= 0 {
		return "", false
	}
	name := f[:i]
	if strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n") {
		// "Bcc : x" is not valid, but some relays accept it. Treat trailing
		// space as part of the name so it is still matched.
		name = strings.TrimRight(name, " \t")
		if strings.ContainsAny(name, " \t\r\n") || name == "" {
			return "", false
		}
	}
	return strings.ToLower(name), true
}

func prefixSubject(field, prefix string) string {
	i := strings.IndexByte(field, ':')
	rest := strings.TrimLeft(field[i+1:], " \t")
	eol := "\n"
	if strings.HasSuffix(field, "\r\n") {
		eol = "\r\n"
	}
	if strings.TrimSpace(rest) == "" {
		return "Subject: " + encodePrefix(prefix) + eol
	}
	return "Subject: " + encodePrefix(prefix) + " " + rest
}

// encodePrefix returns the prefix as header text: ASCII stays as it is, other
// text becomes an RFC 2047 encoded word. The prefix never holds control
// characters (validated before), so it can not end the header.
func encodePrefix(p string) string {
	return mime.QEncoding.Encode("utf-8", strings.TrimSpace(p))
}

func writeOriginal(out *bytes.Buffer, name string, addrs []string, eol string) {
	clean := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if c, ok := infoAddress(a); ok {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return
	}
	out.WriteString(name + ": " + strings.Join(clean, ", ") + eol)
}

// infoAddress returns a recipient for an X-Original-* header, or false when
// it is not a plain address. Untrusted text never reaches a header line
// unless it passed the strict address validator, which has no room for CR,
// LF or any other control character.
func infoAddress(s string) (string, bool) {
	a, err := ParseAddress(s)
	if err != nil {
		return "", false
	}
	return a.String(), true
}

// validPrefix reports whether p is acceptable as a subject prefix.
func validPrefix(p string) bool {
	if utf8.RuneCountInString(p) > MaxSubjectPrefixRunes || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

// ValidSubjectPrefix reports whether p is acceptable as a redirect subject
// prefix: valid UTF-8, no control characters, at most MaxSubjectPrefixRunes.
func ValidSubjectPrefix(p string) bool { return validPrefix(p) }
