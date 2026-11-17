package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	for _, tc := range []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"no proxy", "192.0.2.1:5000", nil, trusted, "192.0.2.1"},
		{"untrusted peer ignores the header", "192.0.2.1:5000", []string{"203.0.113.9"}, trusted, "192.0.2.1"},
		{"no trusted proxies ignores the header", "10.0.0.1:5000", []string{"203.0.113.9"}, nil, "10.0.0.1"},
		{"trusted peer uses the last hop", "10.0.0.1:5000", []string{"203.0.113.9"}, trusted, "203.0.113.9"},
		{"spoofed left hops are skipped", "10.0.0.1:5000", []string{"1.2.3.4, 203.0.113.9"}, trusted, "203.0.113.9"},
		{"chained trusted proxies", "10.0.0.1:5000", []string{"203.0.113.9, 10.1.1.1", "10.2.2.2"}, trusted, "203.0.113.9"},
		{"all hops trusted", "10.0.0.1:5000", []string{"10.3.3.3"}, trusted, "10.3.3.3"},
		{"trusted peer without header", "10.0.0.1:5000", nil, trusted, "10.0.0.1"},
		{"garbage stops at the last trusted hop", "10.0.0.1:5000", []string{"203.0.113.9, nonsense, 10.4.4.4"}, trusted, "10.4.4.4"},
		{"hop with port", "10.0.0.1:5000", []string{"203.0.113.9:1234"}, trusted, "203.0.113.9"},
		{"ipv6 proxy", "[fd00::1]:443", []string{"2001:db8::7"}, trusted, "2001:db8::7"},
		{"mapped ipv4 peer", "[::ffff:10.0.0.1]:443", []string{"203.0.113.9"}, trusted, "203.0.113.9"},
		{"unparseable remote", "pipe", nil, trusted, "pipe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, tc.trusted); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTooManyLoginsRetryAfterFollowsTheWindow(t *testing.T) {
	l := newLoginLimits()
	for range maxLoginsPerUser {
		l.account.Fail("a")
	}
	w := httptest.NewRecorder()
	tooManyLogins(w, l.account, "a")
	if got := w.Header().Get("Retry-After"); got != "300" {
		t.Errorf("Retry-After = %q, want the remaining window (300)", got)
	}
	w = httptest.NewRecorder()
	tooManyLogins(w, l.account, "unblocked")
	if got := w.Header().Get("Retry-After"); got != "300" {
		t.Errorf("Retry-After fallback = %q", got)
	}
}
