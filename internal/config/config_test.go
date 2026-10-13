package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validKey = "0123456789abcdef0123456789abcdef"

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func base() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/db",
		"SECRET_KEY":   validKey,
	}
}

func with(m map[string]string, kv ...string) map[string]string {
	out := make(map[string]string, len(m)+len(kv)/2)
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
		check   func(t *testing.T, c Config)
	}{
		{
			name: "minimal valid applies defaults",
			env:  base(),
			check: func(t *testing.T, c Config) {
				if c.SMTPListen != DefaultSMTPListen || c.HTTPListen != DefaultHTTPListen {
					t.Errorf("listen defaults: %q %q", c.SMTPListen, c.HTTPListen)
				}
				if c.MaxMessageBytes != DefaultMaxMessageBytes {
					t.Errorf("max bytes: %d", c.MaxMessageBytes)
				}
				if c.LogLevel != slog.LevelInfo || c.LogFormat != "text" {
					t.Errorf("log defaults: %v %q", c.LogLevel, c.LogFormat)
				}
			},
		},
		{
			name:    "missing everything",
			env:     map[string]string{},
			wantErr: []string{"DATABASE_URL is required", "SECRET_KEY is required"},
		},
		{
			name:    "missing database url",
			env:     with(base(), "DATABASE_URL", ""),
			wantErr: []string{"DATABASE_URL is required"},
		},
		{
			name:    "whitespace only is missing",
			env:     with(base(), "DATABASE_URL", "   "),
			wantErr: []string{"DATABASE_URL is required"},
		},
		{
			name:    "short secret key",
			env:     with(base(), "SECRET_KEY", "short"),
			wantErr: []string{"SECRET_KEY must be at least 32 bytes"},
		},
		{
			name: "overrides listen addresses",
			env:  with(base(), "SMTP_LISTEN", ":587", "HTTP_LISTEN", "127.0.0.1:9000"),
			check: func(t *testing.T, c Config) {
				if c.SMTPListen != ":587" || c.HTTPListen != "127.0.0.1:9000" {
					t.Errorf("got %q %q", c.SMTPListen, c.HTTPListen)
				}
			},
		},
		{
			name:    "bad listen address",
			env:     with(base(), "SMTP_LISTEN", "nonsense"),
			wantErr: []string{"SMTP_LISTEN"},
		},
		{
			name: "tls pair ok",
			env:  with(base(), "TLS_CERT_FILE", "/c.pem", "TLS_KEY_FILE", "/k.pem"),
			check: func(t *testing.T, c Config) {
				if c.TLSCertFile != "/c.pem" || c.TLSKeyFile != "/k.pem" {
					t.Errorf("tls: %+v", c)
				}
			},
		},
		{
			name:    "tls cert without key",
			env:     with(base(), "TLS_CERT_FILE", "/c.pem"),
			wantErr: []string{"TLS_CERT_FILE and TLS_KEY_FILE must be set together"},
		},
		{
			name:    "tls key without cert",
			env:     with(base(), "TLS_KEY_FILE", "/k.pem"),
			wantErr: []string{"TLS_CERT_FILE and TLS_KEY_FILE must be set together"},
		},
		{
			name: "admin bootstrap pair ok",
			env:  with(base(), "ADMIN_EMAIL", "a@example.test", "ADMIN_PASSWORD", "longenoughpassword"),
			check: func(t *testing.T, c Config) {
				if c.AdminEmail != "a@example.test" || c.AdminPassword != "longenoughpassword" {
					t.Errorf("admin: %+v", c)
				}
			},
		},
		{
			name:    "admin email without password",
			env:     with(base(), "ADMIN_EMAIL", "a@example.test"),
			wantErr: []string{"ADMIN_EMAIL and ADMIN_PASSWORD must be set together"},
		},
		{
			name:    "admin password without email",
			env:     with(base(), "ADMIN_PASSWORD", "longenoughpassword"),
			wantErr: []string{"ADMIN_EMAIL and ADMIN_PASSWORD must be set together"},
		},
		{
			name:    "admin email invalid",
			env:     with(base(), "ADMIN_EMAIL", "nope", "ADMIN_PASSWORD", "longenoughpassword"),
			wantErr: []string{"ADMIN_EMAIL is not a valid email"},
		},
		{
			name:    "admin password too short",
			env:     with(base(), "ADMIN_EMAIL", "a@example.test", "ADMIN_PASSWORD", "short"),
			wantErr: []string{"ADMIN_PASSWORD must be at least 12 characters"},
		},
		{
			name: "max message bytes",
			env:  with(base(), "MAX_MESSAGE_BYTES", "1024"),
			check: func(t *testing.T, c Config) {
				if c.MaxMessageBytes != 1024 {
					t.Errorf("got %d", c.MaxMessageBytes)
				}
			},
		},
		{
			name:    "max message bytes not a number",
			env:     with(base(), "MAX_MESSAGE_BYTES", "big"),
			wantErr: []string{"MAX_MESSAGE_BYTES"},
		},
		{
			name:    "max message bytes not positive",
			env:     with(base(), "MAX_MESSAGE_BYTES", "0"),
			wantErr: []string{"MAX_MESSAGE_BYTES must be positive"},
		},
		{
			name: "log level and format",
			env:  with(base(), "LOG_LEVEL", "DEBUG", "LOG_FORMAT", "json"),
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelDebug || c.LogFormat != "json" {
					t.Errorf("got %v %q", c.LogLevel, c.LogFormat)
				}
			},
		},
		{
			name:    "bad log level",
			env:     with(base(), "LOG_LEVEL", "loud"),
			wantErr: []string{"LOG_LEVEL"},
		},
		{
			name:    "bad log format",
			env:     with(base(), "LOG_FORMAT", "xml"),
			wantErr: []string{"LOG_FORMAT must be one of text, json"},
		},
		{
			name:    "multiple errors are reported together",
			env:     with(base(), "SECRET_KEY", "x", "LOG_FORMAT", "xml"),
			wantErr: []string{"SECRET_KEY", "LOG_FORMAT"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(envFrom(tt.env))
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected error containing %v", tt.wantErr)
				}
				for _, w := range tt.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q missing %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sandpost.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFile(t *testing.T) {
	t.Run("file supplies values", func(t *testing.T) {
		p := writeFile(t, "# comment\n\nDATABASE_URL=postgres://f\nexport SECRET_KEY=\""+validKey+"\"\nHTTP_LISTEN='127.0.0.1:1'\n")
		c, err := Load(envFrom(map[string]string{"SANDPOST_CONFIG_FILE": p}))
		if err != nil {
			t.Fatal(err)
		}
		if c.DatabaseURL != "postgres://f" || c.SecretKey != validKey || c.HTTPListen != "127.0.0.1:1" {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("env overrides file", func(t *testing.T) {
		p := writeFile(t, "DATABASE_URL=postgres://file\n")
		env := with(base(), "SANDPOST_CONFIG_FILE", p)
		c, err := Load(envFrom(env))
		if err != nil {
			t.Fatal(err)
		}
		if c.DatabaseURL != base()["DATABASE_URL"] {
			t.Errorf("env should win, got %q", c.DatabaseURL)
		}
	})
	t.Run("missing file is an error", func(t *testing.T) {
		env := with(base(), "SANDPOST_CONFIG_FILE", filepath.Join(t.TempDir(), "nope"))
		if _, err := Load(envFrom(env)); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("malformed line", func(t *testing.T) {
		p := writeFile(t, "NOEQUALS\n")
		env := with(base(), "SANDPOST_CONFIG_FILE", p)
		_, err := Load(envFrom(env))
		if err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty key", func(t *testing.T) {
		p := writeFile(t, "=x\n")
		env := with(base(), "SANDPOST_CONFIG_FILE", p)
		if _, err := Load(envFrom(env)); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestLogValueRedactsSecrets(t *testing.T) {
	c, err := Load(envFrom(with(base(), "ADMIN_EMAIL", "a@example.test", "ADMIN_PASSWORD", "longenoughpassword")))
	if err != nil {
		t.Fatal(err)
	}
	s := c.LogValue().String()
	for _, secret := range []string{validKey, "longenoughpassword", "u:p@"} {
		if strings.Contains(s, secret) {
			t.Errorf("log value leaks %q: %s", secret, s)
		}
	}
	if !strings.Contains(s, "SMTPListen") && !strings.Contains(s, "smtp_listen") {
		t.Errorf("expected listen address in %s", s)
	}
}

func TestLoadNilGetenv(t *testing.T) {
	if _, err := Load(nil); err == nil {
		t.Fatal("expected required-variable errors")
	}
}
