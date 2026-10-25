package policy_test

import (
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/policy"
)

// rewrite runs a message through a redirect evaluation (the engine path that
// uses every header edit) and returns the redirected bytes.
func redirected(t *testing.T, raw, prefix string, to, cc []string) string {
	t.Helper()
	plan, err := policy.Evaluate(policy.Input{
		Policy:    &policy.Policy{Mode: policy.ModeRedirect, RedirectAddress: "pm@corp.example", SubjectPrefix: prefix},
		Transport: okTransport,
		Message:   policy.Message{Raw: []byte(raw), EnvelopeTo: []string{"x@y.test"}, To: to, Cc: cc},
	})
	if err != nil || len(plan.Outbound) != 1 {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	return string(plan.Outbound[0].Data())
}

func direct(t *testing.T, raw string) string {
	t.Helper()
	plan, err := policy.Evaluate(policy.Input{
		Policy: allowPolicy("@corp.example"), Transport: okTransport,
		Message: policy.Message{Raw: []byte(raw), EnvelopeTo: []string{"a@corp.example"}},
	})
	if err != nil || len(plan.Outbound) != 1 {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	return string(plan.Outbound[0].Data())
}

func TestBccIsStripped(t *testing.T) {
	tests := map[string]string{
		"plain":              "Bcc: a@x.test\r\n",
		"upper case":         "BCC: a@x.test\r\n",
		"mixed case":         "bCc: a@x.test\r\n",
		"space before colon": "Bcc : a@x.test\r\n",
		"folded":             "Bcc: a@x.test,\r\n b@x.test,\r\n\tc@x.test\r\n",
		"duplicated":         "Bcc: a@x.test\r\nBcc: b@x.test\r\n",
	}
	for name, bcc := range tests {
		t.Run(name, func(t *testing.T) {
			raw := "From: a@x.test\r\n" + bcc + "Subject: s\r\n\r\nbody Bcc: kept in body\r\n"
			for _, out := range []string{direct(t, raw), redirected(t, raw, "", nil, nil)} {
				head, body, _ := strings.Cut(out, "\r\n\r\n")
				head += "\r\n"
				if strings.Contains(strings.ToLower(head), "bcc") || strings.Contains(head, "b@x.test") || strings.Contains(head, "c@x.test") {
					t.Errorf("Bcc survived in headers:\n%s", head)
				}
				if body != "body Bcc: kept in body\r\n" {
					t.Errorf("body changed: %q", body)
				}
				if !strings.Contains(head, "From: a@x.test\r\n") || !strings.Contains(head, "Subject: s\r\n") {
					t.Errorf("other headers lost:\n%s", head)
				}
			}
		})
	}
}

func TestSenderSuppliedOriginalHeadersAreRemoved(t *testing.T) {
	raw := "From: a@x.test\r\nX-Original-To: forged@x.test\r\nx-original-cc: forged2@x.test\r\nX-Original-Bcc: f3@x.test\r\nX-Other: keep\r\nSubject: s\r\n\r\nb"
	d := direct(t, raw)
	if strings.Contains(strings.ToLower(d), "x-original") || !strings.Contains(d, "X-Other: keep\r\n") {
		t.Errorf("direct:\n%s", d)
	}
	r := redirected(t, raw, "", []string{"real@corp.example"}, nil)
	if strings.Contains(r, "forged") || !strings.Contains(r, "X-Original-To: real@corp.example\r\n") {
		t.Errorf("redirected:\n%s", r)
	}
}

func TestHeadersArePreserved(t *testing.T) {
	raw := "Received: from a\r\n by b\r\nMessage-ID: <1@x.test>\r\nDate: Mon, 1 Jan 2026 00:00:00 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\nX-Run-Id: 42\r\nSubject: s\r\n\r\n--b\r\nBcc: not-a-header@x.test\r\n\r\nx\r\n--b--\r\n"
	for _, out := range []string{direct(t, raw), redirected(t, raw, "", nil, nil)} {
		for _, h := range []string{"Received: from a\r\n by b\r\n", "Message-ID: <1@x.test>\r\n", "X-Run-Id: 42\r\n", "Content-Type: multipart/mixed; boundary=\"b\"\r\n"} {
			if !strings.Contains(out, h) {
				t.Errorf("header %q lost:\n%s", h, out)
			}
		}
		if !strings.HasSuffix(out, "\r\n\r\n--b\r\nBcc: not-a-header@x.test\r\n\r\nx\r\n--b--\r\n") {
			t.Errorf("body or MIME part headers modified:\n%s", out)
		}
	}
}

func TestLineEndingsAreKept(t *testing.T) {
	out := redirected(t, "From: a@x.test\nSubject: s\nBcc: b@x.test\n\nbody\n", "[x]", []string{"r@corp.example"}, nil)
	if strings.Contains(out, "\r") {
		t.Errorf("an LF message must stay LF:\n%q", out)
	}
	if !strings.Contains(out, "Subject: [x] s\n") || !strings.Contains(out, "X-Original-To: r@corp.example\n") || !strings.HasSuffix(out, "\n\nbody\n") {
		t.Errorf("rewrite:\n%q", out)
	}
}

func TestSubjectPrefix(t *testing.T) {
	tests := []struct {
		name, raw, prefix, want string
	}{
		{"simple", "Subject: Hello\r\n\r\n", "[staging]", "Subject: [staging] Hello\r\n"},
		{"folded subject", "Subject: Hello\r\n world\r\n\r\n", "[s]", "Subject: [s] Hello\r\n world\r\n"},
		{"encoded word subject", "Subject: =?UTF-8?B?w6k=?=\r\n\r\n", "[s]", "Subject: [s] =?UTF-8?B?w6k=?=\r\n"},
		{"empty subject", "Subject:\r\n\r\n", "[s]", "Subject: [s]\r\n"},
		{"missing subject", "From: a@x.test\r\n\r\n", "[s]", "Subject: [s]\r\n"},
		{"upper case name", "SUBJECT: Hi\r\n\r\n", "[s]", "Subject: [s] Hi\r\n"},
		{"non ascii prefix is encoded", "Subject: Hi\r\n\r\n", "[é]", "Subject: =?utf-8?q?[=C3=A9]?= Hi\r\n"},
		{"prefix with surrounding spaces", "Subject: Hi\r\n\r\n", "  [s]  ", "Subject: [s] Hi\r\n"},
		{"no prefix leaves subject", "Subject: Hi\r\n\r\n", "", "Subject: Hi\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := redirected(t, tc.raw, tc.prefix, nil, nil)
			if !strings.Contains(out, tc.want) {
				t.Errorf("want %q in:\n%q", tc.want, out)
			}
			if n := strings.Count(strings.ToLower(out), "subject:"); n != 1 {
				t.Errorf("%d subject headers in:\n%q", n, out)
			}
		})
	}
}

func TestSubjectPrefixAppliesOnlyToTheRedirectedCopy(t *testing.T) {
	plan, _ := policy.Evaluate(policy.Input{
		Policy:    &policy.Policy{Mode: policy.ModeRedirect, Allowlist: []string{"@corp.example"}, RedirectAddress: "pm@corp.example", SubjectPrefix: "[s]"},
		Transport: okTransport,
		Message:   policy.Message{Raw: []byte("Subject: Hi\r\n\r\nb"), EnvelopeTo: []string{"a@corp.example", "z@y.test"}},
	})
	if !strings.Contains(string(plan.Outbound[0].Data()), "Subject: Hi\r\n") || !strings.Contains(string(plan.Outbound[1].Data()), "Subject: [s] Hi\r\n") {
		t.Errorf("prefix on the wrong copy: %q / %q", plan.Outbound[0].Data(), plan.Outbound[1].Data())
	}
}

func TestOriginalHeadersAreSanitised(t *testing.T) {
	out := redirected(t, "Subject: s\r\n\r\nb", "", []string{"a@x.test\r\nBcc: evil@evil.test", "b@x.test"}, []string{"\r\n", "c@x.test"})
	head, _, _ := strings.Cut(out, "\r\n\r\n")
	if strings.Contains(head, "Bcc") || strings.Contains(head, "evil") || strings.Count(head, "\r\n") != 2 {
		t.Errorf("header injection through X-Original-*:\n%q", head)
	}
	if !strings.Contains(head, "X-Original-To: b@x.test\r\n") || !strings.HasSuffix(head, "X-Original-Cc: c@x.test") {
		t.Errorf("sanitised cc lost:\n%q", head)
	}
}

func TestHeaderOnlyAndBodylessMessages(t *testing.T) {
	out := direct(t, "Subject: s\r\nBcc: b@x.test")
	if out != "Subject: s\r\n\r\n" {
		t.Errorf("got %q", out)
	}
	out = direct(t, "")
	if out != "\n" && out != "\r\n" {
		t.Errorf("an empty message becomes a bare separator, got %q", out)
	}
}

func TestValidSubjectPrefix(t *testing.T) {
	for p, want := range map[string]bool{"": true, "[staging] ": true, "é": true, "a\nb": false, "a\rb": false, "a\x00": false,
		"a\u2028b": false, strings.Repeat("x", 100): true, strings.Repeat("x", 101): false, "\xff": false} {
		if got := policy.ValidSubjectPrefix(p); got != want {
			t.Errorf("ValidSubjectPrefix(%q) = %v, want %v", p, got, want)
		}
	}
}
