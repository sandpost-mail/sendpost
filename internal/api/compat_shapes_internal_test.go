package api

import (
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/messages"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		in   string
		want *mpAddress
	}{
		{"", nil},
		{"  ", nil},
		{"alice@example.test", &mpAddress{Address: "alice@example.test"}},
		{"Alice Example <alice@example.test>", &mpAddress{Name: "Alice Example", Address: "alice@example.test"}},
		{`"Doe, Jane" <jane@example.test>`, &mpAddress{Name: "Doe, Jane", Address: "jane@example.test"}},
		{"not an address", &mpAddress{Name: "not an address"}},
	}
	for _, tc := range tests {
		got := parseAddress(tc.in)
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("parseAddress(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("é", 300)
	tests := []struct {
		name, text, html, want string
	}{
		{"text wins", "Hello   there\n\nfriend", "<p>ignored</p>", "Hello there friend"},
		{"html markup removed", "", "<html><head><title>x</title><style>p{}</style></head><body><h1>Head</h1><p>a &amp; b</p><script>x()</script></body></html>", "Head a & b"},
		{"blank text falls back to html", "  \n", "<p>from html</p>", "from html"},
		{"truncated by characters", long, "", strings.Repeat("é", 250)},
		{"empty", "", "", ""},
	}
	for _, tc := range tests {
		if got := snippet(tc.text, tc.html); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestAddressListsFallBackToStoredRecipients(t *testing.T) {
	rs := []messages.Recipient{
		{Kind: "to", Address: "a@x.test"}, {Kind: "cc", Address: "c@x.test"},
		{Kind: "envelope", Address: "A@x.test"}, {Kind: "envelope", Address: "hidden@x.test"}, {Kind: "bcc", Address: "b@x.test"},
	}
	// Without a parsable header the stored addresses are used.
	to := addressesOf(map[string][]string{"To": {"((broken"}}, rs, "To", "to")
	if len(to) != 1 || to[0].Address != "a@x.test" {
		t.Errorf("fallback to: %+v", to)
	}
	cc := addressesOf(nil, rs, "Cc", "cc")
	// Bcc is the envelope and bcc recipients that are not in To or Cc, once each.
	bcc := bccOf(rs, to, cc)
	if len(bcc) != 2 || bcc[0].Address != "hidden@x.test" || bcc[1].Address != "b@x.test" {
		t.Errorf("bcc: %+v", bcc)
	}
	// A parsable header keeps display names.
	named := addressesOf(map[string][]string{"To": {"Bob <bob@x.test>, c@x.test"}}, rs, "To", "to")
	if len(named) != 2 || named[0].Name != "Bob" {
		t.Errorf("named: %+v", named)
	}
	if got := addressesOf(nil, nil, "To", "to"); got == nil || len(got) != 0 {
		t.Errorf("an empty list must not be nil: %#v", got)
	}
}

func TestListUnsubscribe(t *testing.T) {
	tests := []struct {
		name string
		h    map[string][]string
		want mpListUnsubscribe
	}{
		{"none", nil, mpListUnsubscribe{Links: []string{}}},
		{"mailto and https", map[string][]string{
			"List-Unsubscribe":      {"<mailto:u@x.test>, <https://x.test/u>, <https://x.test/second>, <mailto:second@x.test>"},
			"List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"},
		}, mpListUnsubscribe{
			Header:     "<mailto:u@x.test>, <https://x.test/u>, <https://x.test/second>, <mailto:second@x.test>",
			Links:      []string{"mailto:u@x.test", "https://x.test/u"},
			HeaderPost: "List-Unsubscribe=One-Click",
		}},
		{"unsafe scheme ignored", map[string][]string{"List-Unsubscribe": {"<javascript:alert(1)>"}},
			mpListUnsubscribe{Header: "<javascript:alert(1)>", Links: []string{}}},
	}
	for _, tc := range tests {
		got := listUnsubscribe(tc.h)
		if got.Header != tc.want.Header || got.HeaderPost != tc.want.HeaderPost || strings.Join(got.Links, ",") != strings.Join(tc.want.Links, ",") || got.Links == nil {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
}

func TestReturnPathAndDate(t *testing.T) {
	from := &mpAddress{Address: "from@x.test"}
	if got := returnPath(map[string][]string{"Return-Path": {"<bounce@x.test>"}}, from); got != "bounce@x.test" {
		t.Errorf("return path header: %q", got)
	}
	if got := returnPath(nil, from); got != "from@x.test" {
		t.Errorf("return path falls back to From: %q", got)
	}
	if got := returnPath(nil, nil); got != "" {
		t.Errorf("no From: %q", got)
	}
	received := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := messageDate(map[string][]string{"Date": {"Tue, 14 Oct 2026 10:00:00 +0000"}}, received); got.Day() != 14 {
		t.Errorf("date header: %v", got)
	}
	for _, bad := range []map[string][]string{nil, {"Date": {"yesterday"}}} {
		if got := messageDate(bad, received); !got.Equal(received) {
			t.Errorf("without a usable Date header the receipt time is used: %v", got)
		}
	}
}

func TestMessageOfListsOnlyNamedParts(t *testing.T) {
	d := messages.Detail{
		ID: 7, Subject: "s", From: "a@x.test",
		Attachments: []messages.Attachment{
			{Ordinal: 0, Filename: "a.pdf", ContentType: "application/pdf", SizeBytes: 3},
			{Ordinal: 1, Filename: "", ContentID: "", Inline: false},
			{Ordinal: 2, Filename: "", ContentID: "cid1", Inline: true},
		},
	}
	m := messageOf(d, "", map[int]messages.Checksums{0: {MD5: "m"}})
	if len(m.Attachments) != 1 || m.Attachments[0].Checksums.MD5 != "m" || len(m.Inline) != 1 || m.Inline[0].PartID != "2" {
		t.Errorf("%+v", m)
	}
	if m.ID != "7" || m.To == nil || m.Cc == nil || m.Bcc == nil || m.ReplyTo == nil {
		t.Errorf("lists must not be nil: %+v", m)
	}
}
