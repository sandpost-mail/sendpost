package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/policy"
)

func TestParseAddress(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		in      string
		want    string // empty means rejected
		comment string
	}{
		{"dev@example.com", "dev@example.com", ""},
		{"  Dev.Name+tag@Example.COM ", "Dev.Name+tag@example.com", "trimmed, domain lower cased"},
		{"o'brien@example.co.uk", "o'brien@example.co.uk", ""},
		{"", "", "empty"},
		{"plain", "", "no @"},
		{"a@b@example.com", "", "two @"},
		{"@example.com", "", "no local part"},
		{"dev@", "", "no domain"},
		{"dev@localhost", "", "one label"},
		{"dev@example", "", "one label"},
		{"dev@[127.0.0.1]", "", "ip literal"},
		{"dev@127.0.0.1", "", "numeric tld"},
		{"Dev <dev@example.com>", "", "display name"},
		{"<dev@example.com>", "", "angle brackets"},
		{`"dev@evil.com"@example.com`, "", "quoted local part"},
		{`dev@evil.com\@example.com`, "", "escaped @"},
		{"dev%evil.com@example.com", "", "percent hack"},
		{"evil.com!dev@example.com", "", "bang path"},
		{"dev|cmd@example.com", "", "pipe"},
		{"dev@example.com\r\nRCPT TO:<x@y.com>", "", "CRLF injection"},
		{"dev@example.com\n", "dev@example.com", "trailing newline is trimmed whitespace"},
		{"de v@example.com", "", "space inside"},
		{"dev,other@example.com", "", "comma list"},
		{"dev;other@example.com", "", "semicolon list"},
		{".dev@example.com", "", "leading dot"},
		{"dev.@example.com", "", "trailing dot"},
		{"de..v@example.com", "", "double dot"},
		{long + "@example.com", "", "local too long"},
		{"dev@exa_mple.com", "", "underscore in domain"},
		{"dev@-example.com", "", "leading hyphen label"},
		{"dev@example..com", "", "empty label"},
		{"dev@example.com.", "", "trailing dot domain"},
		{"dév@example.com", "", "non ascii local"},
		{"dev@exämple.com", "", "non ascii domain"},
		{"dev\x00@example.com", "", "NUL"},
		{strings.Repeat("a", 60) + "@" + strings.Repeat("b.", 125) + "com", "", "too long overall"},
	}
	for _, tc := range tests {
		t.Run(tc.comment+"/"+tc.in, func(t *testing.T) {
			got, err := policy.ParseAddress(tc.in)
			if tc.want == "" {
				if !errors.Is(err, policy.ErrAddress) {
					t.Fatalf("ParseAddress(%q) = %v, %v; want ErrAddress", tc.in, got, err)
				}
				return
			}
			if err != nil || got.String() != tc.want {
				t.Fatalf("ParseAddress(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeAllowlist(t *testing.T) {
	got, err := policy.NormalizeAllowlist([]string{"Dev@Example.com", "@Corp.example", "corp.example", "dev@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev@example.com", "@corp.example"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	for _, bad := range []string{"", "  ", "*", "*.example.com", "@*.example.com", "dev@*.example.com", "a@b@c.com",
		"example", "dev@example", "Dev <dev@example.com>", "dev@example.com, other@example.com", "dev@exa mple.com", "%@example.com"} {
		if _, err := policy.NormalizeAllowlist([]string{"ok@example.com", bad}); !errors.Is(err, policy.ErrAllowlist) {
			t.Errorf("entry %q must fail the list, got %v", bad, err)
		}
	}
	many := make([]string, policy.MaxAllowlistEntries+1)
	for i := range many {
		many[i] = "a@example.com"
	}
	if _, err := policy.NormalizeAllowlist(many); !errors.Is(err, policy.ErrAllowlist) {
		t.Errorf("an oversized list must fail, got %v", err)
	}
	if got, err := policy.NormalizeAllowlist(nil); err != nil || len(got) != 0 {
		t.Errorf("an empty list is valid: %v %v", got, err)
	}
}
