package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasRecipient(p Parsed, kind, addr string) bool {
	for _, r := range p.Recipients {
		if r.Kind == kind && r.Address == addr {
			return true
		}
	}
	return false
}

func TestParseSimpleText(t *testing.T) {
	p := Parse(fixture(t, "simple_text.eml"), "bounce@example.test", []string{"bob@example.test", "bcc@example.test"})
	if p.Subject != "Plain hello" || p.From != "Alice Example <alice@example.test>" {
		t.Errorf("subject/from: %q %q", p.Subject, p.From)
	}
	if p.MessageID != "<simple-1@example.test>" {
		t.Errorf("message id %q", p.MessageID)
	}
	for _, want := range [][2]string{
		{KindTo, "bob@example.test"}, {KindTo, "carol@example.test"}, {KindCc, "dave@example.test"},
		{KindEnvelope, "bcc@example.test"}, {KindEnvelope, "bob@example.test"},
	} {
		if !hasRecipient(p, want[0], want[1]) {
			t.Errorf("missing recipient %v in %+v", want, p.Recipients)
		}
	}
	if got := p.Headers["X-Run-Id"]; len(got) != 1 || got[0] != "run-42" {
		t.Errorf("headers: %+v", p.Headers)
	}
	wantLinks := []string{"https://example.test/welcome?token=abc123", "https://example.test/docs"}
	if strings.Join(p.Links, "|") != strings.Join(wantLinks, "|") {
		t.Errorf("links %v want %v", p.Links, wantLinks)
	}
}

func TestParseMultipartAlternativeLinks(t *testing.T) {
	p := Parse(fixture(t, "multipart_alternative.eml"), "", nil)
	if !strings.Contains(p.Text, "Confirm:") || !strings.Contains(p.HTML, "<p>Hi</p>") {
		t.Fatalf("bodies: %q / %q", p.Text, p.HTML)
	}
	want := []string{
		"https://example.test/confirm?x=1", "mailto:support@example.test", "https://example.test/plain-only",
	}
	if strings.Join(p.Links, "|") != strings.Join(want, "|") {
		t.Errorf("links %v want %v", p.Links, want)
	}
}

func TestParseAttachmentsAndInline(t *testing.T) {
	raw := fixture(t, "attachments_inline.eml")
	p := Parse(raw, "", nil)
	if len(p.Attachments) != 2 {
		t.Fatalf("attachments: %+v", p.Attachments)
	}
	pdf, logo := p.Attachments[0], p.Attachments[1]
	if pdf.Filename != "report.pdf" || pdf.ContentType != "application/pdf" || pdf.Inline || pdf.Size == 0 {
		t.Errorf("pdf: %+v", pdf)
	}
	if logo.Filename != "logo.png" || logo.ContentID != "logo123" || !logo.Inline || logo.ContentType != "image/png" {
		t.Errorf("logo: %+v", logo)
	}

	a, content, ok := AttachmentContent(raw, 0)
	if !ok || a.Filename != "report.pdf" || string(content) != "%PDF-1.4 fake pdf bytes" {
		t.Errorf("content: %+v %q %v", a, content, ok)
	}
	if _, _, ok := AttachmentContent(raw, 2); ok {
		t.Error("ordinal 2 must not exist")
	}
	if _, _, ok := AttachmentContent(raw, -1); ok {
		t.Error("negative ordinal must not exist")
	}
}

func TestAttachmentBytes(t *testing.T) {
	raw := fixture(t, "attachments_inline.eml")
	got := AttachmentBytes(raw)
	if len(got) != 2 || string(got[0]) != "%PDF-1.4 fake pdf bytes" || len(got[1]) == 0 {
		t.Errorf("bytes: %q", got)
	}
	if got := AttachmentBytes(fixture(t, "simple_text.eml")); len(got) != 0 {
		t.Errorf("a message without attachments: %q", got)
	}
}

func TestParseCharsets(t *testing.T) {
	p := Parse(fixture(t, "charset_latin1.eml"), "", nil)
	if p.Subject != "Café résumé crème" {
		t.Errorf("subject %q", p.Subject)
	}
	if !strings.Contains(p.Text, "Café crème brûlée") {
		t.Errorf("text %q", p.Text)
	}
	if !strings.Contains(p.From, "André Dupont") || !strings.Contains(p.From, "andre@example.test") {
		t.Errorf("from %q", p.From)
	}
}

func TestParseHTMLOnlyDerivesText(t *testing.T) {
	p := Parse(fixture(t, "html_only.eml"), "", nil)
	if p.HTML == "" || !strings.Contains(p.Text, "Headline") {
		t.Errorf("html-only: text %q html %q", p.Text, p.HTML)
	}
	if len(p.Links) != 1 || p.Links[0] != "https://example.test/article" {
		t.Errorf("links %v", p.Links)
	}
}

func TestParseNeverFails(t *testing.T) {
	for _, name := range []string{"malformed_no_headers.eml", "multipart_missing_boundary.eml", "nul_and_invalid.eml"} {
		t.Run(name, func(t *testing.T) {
			p := Parse(fixture(t, name), "env@example.test", []string{"rcpt@example.test"})
			if !hasRecipient(p, KindEnvelope, "rcpt@example.test") {
				t.Errorf("envelope recipient lost: %+v", p.Recipients)
			}
			if p.From == "" {
				t.Error("from must fall back to the envelope sender")
			}
		})
	}
	if Parse(nil, "", nil).Headers == nil {
		t.Error("headers must never be nil")
	}
}

func TestParseOutputIsPostgresSafe(t *testing.T) {
	p := Parse(fixture(t, "nul_and_invalid.eml"), "", nil)
	for name, s := range map[string]string{"subject": p.Subject, "text": p.Text} {
		if strings.ContainsRune(s, 0) || !validUTF8(s) {
			t.Errorf("%s not postgres safe: %q", name, s)
		}
	}
}

func TestParseHugeHeaders(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + strings.Repeat("x", 5000) + "\r\n")
	b.WriteString("X-Huge: " + strings.Repeat("y", 300_000) + "\r\n")
	for i := 0; i < 2500; i++ {
		b.WriteString("X-Many-" + strings.Repeat("a", i%7) + string(rune('A'+i%26)) + ": v\r\n")
	}
	b.WriteString("\r\nbody\r\n")
	p := Parse([]byte(b.String()), "", nil)
	if got := len([]rune(p.Subject)); got != maxSubjectRunes {
		t.Errorf("subject runes %d", got)
	}
	if v := p.Headers["X-Huge"]; len(v) != 1 || len(v[0]) > maxHeaderValue {
		t.Errorf("huge header not bounded: %d", len(v))
	}
	if len(p.Headers) > maxHeaderCount {
		t.Errorf("too many headers: %d", len(p.Headers))
	}
}

func TestParseManyParts(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.test\r\nSubject: parts\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n")
	for i := 0; i < maxParsedParts+50; i++ {
		b.WriteString("--B\r\nContent-Type: text/plain; name=\"f.txt\"\r\nContent-Disposition: attachment; filename=\"f.txt\"\r\n\r\nx\r\n")
	}
	b.WriteString("--B--\r\n")
	p := Parse([]byte(b.String()), "", nil) // must not panic or hang
	if len(p.Attachments) > maxParsedParts {
		t.Errorf("attachments not bounded: %d", len(p.Attachments))
	}
}

func validUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestParseCapsValuesPerHeader(t *testing.T) {
	var b strings.Builder
	for range maxValuesPerHeader * 3 {
		b.WriteString("X-Tag: v\r\n")
	}
	b.WriteString("Subject: s\r\n\r\nbody")
	p := Parse([]byte(b.String()), "", nil)
	if got := len(p.Headers["X-Tag"]); got != maxValuesPerHeader {
		t.Fatalf("values %d want %d", got, maxValuesPerHeader)
	}
}

// htmlOnlyWithAttachment has no text/plain part, so Parse down converts the
// HTML while attachment downloads skip that work.
const htmlOnlyWithAttachment = "From: a@x.test\r\nSubject: h\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/html\r\n\r\n<p>Hi <a href=\"https://x.test\">x</a></p>\r\n" +
	"--B\r\nContent-Type: text/csv\r\nContent-Disposition: attachment; filename=\"a.csv\"\r\n\r\na,b\r\n1,2\r\n" +
	"--B--\r\n"

func TestAttachmentContentMatchesParse(t *testing.T) {
	inputs := map[string][]byte{"html_only_with_attachment": []byte(htmlOnlyWithAttachment)}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			inputs[e.Name()] = fixture(t, e.Name())
		}
	}
	for name, raw := range inputs {
		t.Run(name, func(t *testing.T) {
			want := Parse(raw, "", nil).Attachments
			all := AttachmentBytes(raw)
			if len(all) != len(want) {
				t.Fatalf("AttachmentBytes %d parts, Parse %d", len(all), len(want))
			}
			for i, meta := range want {
				a, content, ok := AttachmentContent(raw, i)
				if !ok || a != meta || int64(len(content)) != meta.Size || string(content) != string(all[i]) {
					t.Errorf("ordinal %d: %+v ok=%v want %+v", i, a, ok, meta)
				}
			}
			if _, _, ok := AttachmentContent(raw, len(want)); ok {
				t.Error("ordinal past the end must not be found")
			}
		})
	}
	if got := Parse([]byte(htmlOnlyWithAttachment), "", nil); !strings.Contains(got.Text, "Hi") || len(got.Attachments) != 1 {
		t.Fatalf("fixture sanity: %+v", got)
	}
}
