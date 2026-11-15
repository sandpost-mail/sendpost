package ingest

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// RealisticMessage is a transactional mail as CI tests capture them: a
// multipart/alternative body with a link heavy HTML part, an inline logo and
// a PDF attachment, about 200 KiB in total. Exported for the ingest_test
// benchmarks.
func RealisticMessage() []byte {
	var html, text strings.Builder
	for i := range 200 {
		fmt.Fprintf(&html, `<tr><td><a href="https://app.example.test/orders/%d?utm_source=mail&amp;u=%d">Order %d</a></td></tr>`, i, i, i)
		fmt.Fprintf(&text, "Order %d: https://app.example.test/orders/%d\n", i, i)
	}
	attach := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("%PDF-1.7 sandpost benchmark "), 5000))
	logo := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 2000))
	var b strings.Builder
	b.WriteString("From: Shop <noreply@shop.example.test>\r\nTo: alice@example.test, bob@example.test\r\n" +
		"Cc: carol@example.test\r\nSubject: Your orders this week\r\nMessage-ID: <bench-1@shop.example.test>\r\n" +
		"X-Run-Id: run-42\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=MIX\r\n\r\n")
	b.WriteString("--MIX\r\nContent-Type: multipart/related; boundary=REL\r\n\r\n")
	b.WriteString("--REL\r\nContent-Type: multipart/alternative; boundary=ALT\r\n\r\n")
	b.WriteString("--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYour code is 482913.\r\n" + text.String() + "\r\n")
	b.WriteString("--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><img src=\"cid:logo\"><table>" +
		html.String() + "</table></body></html>\r\n--ALT--\r\n")
	b.WriteString("--REL\r\nContent-Type: image/png\r\nContent-ID: <logo>\r\nContent-Disposition: inline; filename=logo.png\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + logo + "\r\n--REL--\r\n")
	b.WriteString("--MIX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"invoice.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + attach + "\r\n--MIX--\r\n")
	return []byte(b.String())
}

func corpus(tb testing.TB) [][]byte {
	tb.Helper()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		tb.Fatal(err)
	}
	var out [][]byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("testdata", e.Name())) //nolint:gosec // test fixture path
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, raw)
	}
	return out
}

func FuzzParse(f *testing.F) {
	for _, raw := range corpus(f) {
		f.Add(raw, "bounce@example.test", "rcpt@example.test")
	}
	f.Add([]byte(htmlOnlyWithAttachment), "", "")
	f.Fuzz(func(t *testing.T, raw []byte, from, to string) {
		orig := bytes.Clone(raw)
		p := Parse(raw, from, []string{to})
		if !bytes.Equal(raw, orig) {
			t.Fatal("Parse modified its input")
		}
		checkStorable(t, p)
		if addr := truncateRunes(clean(strings.TrimSpace(to)), maxAddressRunes); addr != "" && !hasEnvelopeFold(p, addr) {
			t.Errorf("envelope recipient %q lost: %+v", addr, p.Recipients)
		}
		all := AttachmentBytes(raw)
		if len(all) != len(p.Attachments) {
			t.Fatalf("AttachmentBytes %d, Parse %d", len(all), len(p.Attachments))
		}
		for i, a := range p.Attachments {
			if a.Ordinal != i || a.Size != int64(len(all[i])) {
				t.Errorf("attachment %d: %+v size %d", i, a, len(all[i]))
			}
		}
	})
}

// hasEnvelopeFold reports whether addr is an envelope recipient, or was
// deduplicated against the same address in a header (case insensitive).
func hasEnvelopeFold(p Parsed, addr string) bool {
	for _, r := range p.Recipients {
		if strings.EqualFold(r.Address, addr) {
			return true
		}
	}
	return false
}

// checkStorable asserts every derived field fits Postgres and the size limits.
func checkStorable(t *testing.T, p Parsed) {
	t.Helper()
	ok := func(field, s string) {
		if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
			t.Errorf("%s not storable: %q", field, s)
		}
	}
	for field, s := range map[string]string{
		"message_id": p.MessageID, "from": p.From, "subject": p.Subject, "text": p.Text, "html": p.HTML,
	} {
		ok(field, s)
	}
	if utf8.RuneCountInString(p.Subject) > maxSubjectRunes || utf8.RuneCountInString(p.From) > maxAddressRunes {
		t.Errorf("subject or from over its limit")
	}
	if len(p.Headers) > maxHeaderCount {
		t.Errorf("%d headers", len(p.Headers))
	}
	for k, vs := range p.Headers {
		ok("header name", k)
		if len(vs) > maxValuesPerHeader {
			t.Errorf("header %q has %d values", k, len(vs))
		}
		for _, v := range vs {
			ok("header value", v)
			if len(v) > maxHeaderValue {
				t.Errorf("header %q value of %d bytes", k, len(v))
			}
		}
	}
	for _, r := range p.Recipients {
		ok("recipient", r.Address)
	}
	for _, a := range p.Attachments {
		ok("filename", a.Filename)
		ok("content type", a.ContentType)
		ok("content id", a.ContentID)
	}
	if len(p.Links) > maxLinks {
		t.Errorf("%d links", len(p.Links))
	}
	for _, l := range p.Links {
		ok("link", l)
	}
}

func BenchmarkParse(b *testing.B) {
	realistic := RealisticMessage()
	b.Run("realistic", func(b *testing.B) {
		b.SetBytes(int64(len(realistic)))
		b.ReportAllocs()
		for b.Loop() {
			Parse(realistic, "bounce@example.test", []string{"alice@example.test"})
		}
	})
	b.Run("testdata_corpus", func(b *testing.B) {
		msgs := corpus(b)
		b.SetBytes(int64(len(bytes.Join(msgs, nil))))
		b.ReportAllocs()
		for b.Loop() {
			for _, raw := range msgs {
				Parse(raw, "bounce@example.test", []string{"alice@example.test"})
			}
		}
	})
	b.Run("attachment_content", func(b *testing.B) {
		b.SetBytes(int64(len(realistic)))
		b.ReportAllocs()
		for b.Loop() {
			if _, _, ok := AttachmentContent(realistic, 1); !ok {
				b.Fatal("attachment not found")
			}
		}
	})
}
