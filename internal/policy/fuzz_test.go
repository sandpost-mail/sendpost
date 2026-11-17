package policy

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

// FuzzParseAddress checks that every accepted address is a plain addr-spec
// that net/mail reads the same way, and that the canonical form is stable.
func FuzzParseAddress(f *testing.F) {
	for _, s := range []string{
		"dev@example.com", " Dev+tag@Example.COM ", "a@b@c.test", "\"q\"@x.test", "a@[127.0.0.1]",
		"a%b@x.test", "Dev <dev@x.test>", "a@x.test\r\nBcc: e@x.test", "é@x.test", "a@x..test", "a(c)@x.test",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := ParseAddress(s)
		if err != nil {
			return
		}
		canon := a.String()
		if strings.ContainsAny(canon, " \t\r\n\x00<>()\",;:[]\\") {
			t.Fatalf("accepted %q with a special character: %q", s, canon)
		}
		again, err := ParseAddress(canon)
		if err != nil || again != a {
			t.Fatalf("canonical form %q does not round-trip: %v %v", canon, again, err)
		}
		m, err := mail.ParseAddress(canon)
		if err != nil {
			t.Fatalf("net/mail rejects accepted %q: %v", canon, err)
		}
		if m.Name != "" || !strings.EqualFold(m.Address, canon) {
			t.Fatalf("net/mail reads %q as %q (name %q)", canon, m.Address, m.Name)
		}
	})
}

// FuzzNormalizeAllowlist checks the allowlist gate end to end: an address
// that fails parsing, or an entry that fails validation, never lets mail
// through, and an address is delivered only when it matches the entry
// exactly (address rule) or by its whole domain (domain rule).
func FuzzNormalizeAllowlist(f *testing.F) {
	for _, s := range [][2]string{
		{"@corp.example", "a@corp.example"}, {"corp.example", "a@sub.corp.example"},
		{"Dev@Corp.Example", "dev@corp.example"}, {"*@corp.example", "a@corp.example"},
		{"@corp.example", "a@corp.example.evil.test"}, {"a@corp.example", "a@corp.example\r\n"},
		{"@corp.example", "\"a@corp.example\"@evil.test"}, {"", "a@x.test"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, rule, rcpt string) {
		norm, nerr := NormalizeAllowlist([]string{rule})
		addr, aerr := ParseAddress(rcpt)
		plan, err := Evaluate(Input{
			Policy:    &Policy{Mode: ModeAllow, Allowlist: []string{rule}},
			Transport: TransportState{Present: true, Enabled: true},
			Message:   Message{Raw: []byte("Subject: s\r\n\r\nb"), EnvelopeTo: []string{rcpt}},
		})
		if nerr != nil || aerr != nil {
			if err == nil || len(plan.Outbound) != 0 {
				t.Fatalf("rule %q (%v) rcpt %q (%v): want blocked, got %+v %v", rule, nerr, rcpt, aerr, plan, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid rule %q and rcpt %q blocked: %v", rule, rcpt, err)
		}
		want := matchesRule(norm[0], addr)
		if got := len(plan.Outbound) == 1; got != want {
			t.Fatalf("rule %q rcpt %q: delivered %v, want %v", norm[0], addr, got, want)
		}
		for _, ob := range plan.Outbound {
			for _, r := range ob.Recipients() {
				if r != addr.String() {
					t.Fatalf("envelope %q is not the parsed recipient %q", r, addr)
				}
			}
		}
	})
}

// matchesRule is an independent model of the allowlist: a canonical rule is
// "local@domain" (exact, case-insensitive local part) or "@domain".
func matchesRule(rule string, a Address) bool {
	local, domain, _ := strings.Cut(rule, "@")
	if domain != a.Domain() {
		return false
	}
	return local == "" || local == strings.ToLower(a.Local())
}

// maxHeaderLineOctets is the RFC 5322 2.1.1 hard line limit.
const maxHeaderLineOctets = 998

// FuzzRewriteHeaders checks the header rewrite invariants on arbitrary input:
// no Bcc, Resent-Bcc or sender-supplied X-Original-* header survives, the
// body is byte-identical, and generated lines stay short.
func FuzzRewriteHeaders(f *testing.F) {
	f.Add([]byte("Bcc: a@x.test\r\nSubject: s\r\n\r\nbody"), "[s]", "a@x.test")
	f.Add([]byte("Resent-Bcc: a@x.test\nX-Original-From: f@x.test\nSubject: s\n\nbody"), "é", "b@x.test")
	f.Add([]byte("Bcc : a\r\n b@x.test\r\nSubject:\r\n\r\n"), "", "")
	f.Add([]byte(""), "[staging]", "c@x.test")
	f.Fuzz(func(t *testing.T, raw []byte, prefix, to string) {
		if !validPrefix(prefix) {
			prefix = ""
		}
		tos := strings.Split(to, ",")
		out := rewriteHeaders(raw, headerEdit{subjectPrefix: strings.TrimSpace(prefix), originalTo: tos, originalCc: tos})

		_, _, inBody := splitMessage(raw)
		outHead, _, outBody := splitMessage(out)
		if !bytes.Equal(inBody, outBody) {
			t.Fatalf("body changed:\n%q\n%q", inBody, outBody)
		}
		added := 0
		for _, f := range splitFields(outHead) {
			name, ok := fieldName(f)
			if !ok {
				continue
			}
			if name == "bcc" || name == "resent-bcc" {
				t.Fatalf("%s survived: %q", name, f)
			}
			if strings.HasPrefix(name, "x-original-") {
				if name != "x-original-to" && name != "x-original-cc" {
					t.Fatalf("sender-supplied %s survived: %q", name, f)
				}
				added++
				for _, line := range strings.Split(strings.TrimRight(f, "\r\n"), "\n") {
					// One address (at most 254 bytes) per line in the worst case.
					if len(strings.TrimRight(line, "\r")) > maxHeaderLineOctets {
						t.Fatalf("generated line too long (%d): %q", len(line), line)
					}
				}
			}
		}
		if added > 2 {
			t.Fatalf("%d X-Original-* headers, at most two are generated", added)
		}
	})
}
