package policy

import (
	"bytes"
	"mime"
	"strconv"
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

// stripped reports the headers always removed from outbound mail: Bcc and
// Resent-Bcc must not reveal blind recipients, and every X-Original-* is
// ours, a sender must not be able to forge any of them.
func stripped(name string) bool {
	return name == "bcc" || name == "resent-bcc" || strings.HasPrefix(name, "x-original-")
}

// Header layout limits (RFC 5322 2.1.1). Generated lines are folded at
// foldLimit; maxOriginalAddrs bounds the addresses listed in one
// X-Original-* header, the rest are summarised in a comment.
const (
	foldLimit        = 78
	maxOriginalAddrs = 50
)

// parsedMessage is a message split once into header fields and body, so an
// evaluation that writes several copies parses its input only once.
type parsedMessage struct {
	fields []string
	sep    string
	eol    string
	body   []byte
	size   int
}

func parseMessage(raw []byte) parsedMessage {
	head, sep, body := splitMessage(raw)
	return parsedMessage{fields: splitFields(head), sep: sep, eol: lineEnding(head), body: body, size: len(raw)}
}

// rewriteHeaders strips Bcc, Resent-Bcc and any X-Original-* headers,
// optionally prefixes the subject and adds X-Original-To and X-Original-Cc.
// It returns a new slice.
func rewriteHeaders(raw []byte, e headerEdit) []byte { return parseMessage(raw).rewrite(e) }

func (m parsedMessage) rewrite(e headerEdit) []byte {
	var out bytes.Buffer
	out.Grow(m.size + 256)
	subjectDone := false
	for _, f := range m.fields {
		name, ok := fieldName(f)
		if ok && stripped(name) {
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
		out.WriteString(prefixSubject("Subject:"+m.eol, e.subjectPrefix))
	}
	writeOriginal(&out, "X-Original-To", e.originalTo, m.eol)
	writeOriginal(&out, "X-Original-Cc", e.originalCc, m.eol)
	sep := m.sep
	if sep == "" {
		sep = m.eol // a message that is only headers still needs the blank line
	}
	out.WriteString(sep)
	out.Write(m.body)
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
			if line == "\r\n" || line == "\n" {
				continue // a lone trailing CR would turn into the body separator
			}
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

// prefixSubject writes the subject with the prefix in front, folded so that
// no generated line passes foldLimit. The original text follows unchanged, on
// a continuation line when it does not fit on the last generated one.
func prefixSubject(field, prefix string) string {
	i := strings.IndexByte(field, ':')
	rest := strings.TrimLeft(field[i+1:], " \t")
	eol := "\n"
	if strings.HasSuffix(field, "\r\n") {
		eol = "\r\n"
	}
	head, last := foldWords("Subject:", strings.Split(encodePrefix(prefix), " "), eol)
	if strings.TrimSpace(rest) == "" {
		return head + eol
	}
	first, _, _ := strings.Cut(rest, "\n")
	if last+1+len(strings.TrimRight(first, "\r")) > foldLimit {
		return head + eol + " " + rest
	}
	return head + " " + rest
}

// encodePrefix returns the prefix as header text: ASCII stays as it is, other
// text becomes RFC 2047 encoded words of at most 75 characters each. The
// prefix never holds control characters (validated before), so it can not end
// the header.
func encodePrefix(p string) string {
	return mime.QEncoding.Encode("utf-8", strings.TrimSpace(p))
}

// foldWords writes name followed by the words, each after one space, and
// starts a continuation line before a word that would pass foldLimit. It
// returns the text without a final line ending and the length of its last
// line.
func foldWords(name string, words []string, eol string) (string, int) {
	var b strings.Builder
	b.WriteString(name)
	line := len(name)
	for _, w := range words {
		// A word longer than a whole line stays alone on its line.
		if line > 0 && line+1+len(w) > foldLimit {
			b.WriteString(eol)
			line = 0
		}
		b.WriteByte(' ')
		b.WriteString(w)
		line += 1 + len(w)
	}
	return b.String(), line
}

func writeOriginal(out *bytes.Buffer, name string, addrs []string, eol string) {
	words := make([]string, 0, min(len(addrs), maxOriginalAddrs)+2)
	more := 0
	for _, a := range addrs {
		c, ok := infoAddress(a)
		switch {
		case !ok:
		case len(words) < maxOriginalAddrs:
			words = append(words, c)
		default:
			more++
		}
	}
	if len(words) == 0 {
		return
	}
	for i := range words[:len(words)-1] {
		words[i] += ","
	}
	if more > 0 {
		words = append(words, "("+strconv.Itoa(more), "more)")
	}
	text, _ := foldWords(name+":", words, eol)
	out.WriteString(text + eol)
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
