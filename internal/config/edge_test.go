package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/smtpd"
)

func TestEdgeSettings(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, err := Load(envFrom(base()))
		if err != nil {
			t.Fatal(err)
		}
		if c.SMTPMaxConnections != DefaultSMTPMaxConnections || c.TrustedProxies != nil || c.RelayDenyCIDRs != nil {
			t.Errorf("defaults: %d %v %v", c.SMTPMaxConnections, c.TrustedProxies, c.RelayDenyCIDRs)
		}
	})
	t.Run("parsed", func(t *testing.T) {
		c, err := Load(envFrom(with(base(),
			"SMTP_MAX_CONNECTIONS", "7",
			"TRUSTED_PROXIES", "10.0.0.0/8, 192.0.2.5 ,fd00::1/8,",
			"RELAY_DENY_CIDRS", "169.254.0.0/16,::ffff:127.0.0.1",
		)))
		if err != nil {
			t.Fatal(err)
		}
		if c.SMTPMaxConnections != 7 {
			t.Errorf("SMTPMaxConnections = %d", c.SMTPMaxConnections)
		}
		wantProxies := []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.5/32"), netip.MustParsePrefix("fd00::/8"),
		}
		if !equalPrefixes(c.TrustedProxies, wantProxies) {
			t.Errorf("TrustedProxies = %v", c.TrustedProxies)
		}
		wantDeny := []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("127.0.0.1/32")}
		if !equalPrefixes(c.RelayDenyCIDRs, wantDeny) {
			t.Errorf("RelayDenyCIDRs = %v", c.RelayDenyCIDRs)
		}
	})
	for _, tc := range []struct{ name, key, val, want string }{
		{"connections not a number", "SMTP_MAX_CONNECTIONS", "many", "SMTP_MAX_CONNECTIONS is not an integer"},
		{"connections zero", "SMTP_MAX_CONNECTIONS", "0", "SMTP_MAX_CONNECTIONS must be positive"},
		{"bad proxy", "TRUSTED_PROXIES", "10.0.0.0/8,proxy.local", "TRUSTED_PROXIES"},
		{"bad prefix length", "TRUSTED_PROXIES", "10.0.0.0/33", "TRUSTED_PROXIES"},
		{"zoned address", "RELAY_DENY_CIDRS", "fe80::1%eth0", "RELAY_DENY_CIDRS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envFrom(with(base(), tc.key, tc.val)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func equalPrefixes(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSecretKeyEntropy(t *testing.T) {
	for _, tc := range []struct {
		key string
		ok  bool
	}{
		{strings.Repeat("a", 64), false},
		{strings.Repeat("ab", 32), false},
		{strings.Repeat("abcdefg", 6), false},
		{strings.Repeat("abcdefgh", 4), true},
		{validKey, true},
	} {
		_, err := Load(envFrom(with(base(), "SECRET_KEY", tc.key)))
		if (err == nil) != tc.ok {
			t.Errorf("key %q: err = %v, want ok=%v", tc.key, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "openssl rand") {
			t.Errorf("error should explain how to generate a key: %v", err)
		}
	}
}

// TestTLSModesMatchSMTPD keeps the config values and the values the SMTP
// server accepts in sync, now that smtpd no longer imports config.
func TestTLSModesMatchSMTPD(t *testing.T) {
	pairs := [][2]string{
		{TLSOff, smtpd.TLSOff}, {TLSStartTLS, smtpd.TLSStartTLS},
		{TLSRequired, smtpd.TLSRequired}, {TLSImplicit, smtpd.TLSImplicit},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("config %q != smtpd %q", p[0], p[1])
		}
	}
}

func TestParseFile(t *testing.T) {
	for _, tc := range []struct {
		name, in, key, want string
	}{
		{"plain", "K=v", "K", "v"},
		{"export", "export K=v", "K", "v"},
		{"double quotes", `K="a b"`, "K", "a b"},
		{"single quotes", `K='a b'`, "K", "a b"},
		{"quoted with comment", `K="a#b" # prod`, "K", "a#b"},
		{"unquoted with comment", "K=abc # prod", "K", "abc"},
		{"unquoted tab comment", "K=abc\t# prod", "K", "abc"},
		{"hash inside word kept", "K=pass#word", "K", "pass#word"},
		{"only a comment", "K= # nothing", "K", ""},
		{"empty", "K=", "K", ""},
		{"quotes kept literally", `K="a\nb"`, "K", `a\nb`},
		{"inner quote kept", `K=it's`, "K", "it's"},
		{"crlf", "K=v\r\n", "K", "v"},
		{"equals in value", "K=a=b", "K", "a=b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFile(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got[tc.key] != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got[tc.key], tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, in, want string }{
		{"unterminated", `K="abc`, "line 1: K: unterminated"},
		{"trailing text", `K="abc" x`, "only a # comment"},
		{"no equals", "K", "line 1"},
		{"space in key", "A B=c", "line 1"},
		{"later line", "A=b\nC='d", "line 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFile(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix permissions")
	}
	write := func(t *testing.T, content string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "sandpost.env")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("world readable with a secret is refused", func(t *testing.T) {
		p := write(t, "SECRET_KEY="+validKey+"\n", 0o644)
		_, err := Load(envFrom(map[string]string{"SANDPOST_CONFIG_FILE": p, "DATABASE_URL": "postgres://x"}))
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("group readable with admin password is refused", func(t *testing.T) {
		p := write(t, "ADMIN_PASSWORD=whatever\n", 0o640)
		if _, err := Load(envFrom(with(base(), "SANDPOST_CONFIG_FILE", p))); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("readable without secrets is fine", func(t *testing.T) {
		p := write(t, "HTTP_LISTEN=:9000\n", 0o644)
		if _, err := Load(envFrom(with(base(), "SANDPOST_CONFIG_FILE", p))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("private file with secrets is fine", func(t *testing.T) {
		p := write(t, "SECRET_KEY="+validKey+"\n", 0o400)
		if _, err := Load(envFrom(map[string]string{"SANDPOST_CONFIG_FILE": p, "DATABASE_URL": "postgres://x"})); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzParseConfigFile(f *testing.F) {
	for _, seed := range []string{
		"", "K=v", "export K=\"v\" # c", "K='v'", "# c\n\nA=b\r\nC=d",
		"K=\"unterminated", "=x", "K=a #b", "K= #", "K='a'b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		vals, err := parseFile(data)
		if err != nil {
			return
		}
		for k, v := range vals {
			if k == "" || strings.ContainsAny(k, "=\n") {
				t.Errorf("bad key %q", k)
			}
			if strings.Contains(v, "\n") {
				t.Errorf("value %q spans lines", v)
			}
		}
		// Rendering the parsed values back as quoted lines must round trip
		// when the value has no quote of the chosen kind.
		var sb strings.Builder
		for k, v := range vals {
			if strings.Contains(v, `"`) || strings.Contains(k, "#") || strings.HasPrefix(k, "export ") {
				continue
			}
			sb.WriteString(k + `="` + v + "\"\n")
		}
		again, err := parseFile(sb.String())
		if err != nil {
			t.Fatalf("re-parse of %q: %v", sb.String(), err)
		}
		for k, v := range again {
			if vals[k] != v {
				t.Errorf("round trip %s: %q != %q", k, v, vals[k])
			}
		}
	})
}
