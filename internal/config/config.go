// Package config loads Sandpost configuration from environment variables and
// an optional KEY=VALUE file. Environment variables always win over the file.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
)

// Defaults for optional settings.
const (
	DefaultSMTPListen            = ":1025"
	DefaultHTTPListen            = ":8080"
	DefaultMaxMessageBytes int64 = 25 << 20
	minSecretKeyBytes            = 32
	minAdminPasswordLength       = 12
	configFileVar                = "SANDPOST_CONFIG_FILE"
	logFormatText                = "text"
	logFormatJSON                = "json"
)

// SMTP TLS modes (SMTP_TLS_MODE).
const (
	// TLSOff serves plaintext SMTP only.
	TLSOff = "off"
	// TLSStartTLS offers STARTTLS, plaintext AUTH stays allowed.
	TLSStartTLS = "starttls"
	// TLSRequired offers STARTTLS and refuses AUTH before TLS.
	TLSRequired = "required"
	// TLSImplicit wraps the whole connection in TLS (SMTPS).
	TLSImplicit = "implicit"
)

// Config is the validated, immutable runtime configuration.
type Config struct {
	DatabaseURL     string
	SecretKey       string
	SMTPListen      string
	HTTPListen      string
	TLSCertFile     string
	TLSKeyFile      string
	SMTPTLSMode     string
	AdminEmail      string
	AdminPassword   string
	MaxMessageBytes int64
	LogLevel        slog.Level
	LogFormat       string
}

// LogValue implements slog.LogValuer and never exposes secrets.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("SMTPListen", c.SMTPListen),
		slog.String("HTTPListen", c.HTTPListen),
		slog.Bool("TLS", c.TLSCertFile != ""),
		slog.String("SMTPTLSMode", c.SMTPTLSMode),
		slog.Bool("AdminBootstrap", c.AdminEmail != ""),
		slog.Int64("MaxMessageBytes", c.MaxMessageBytes),
		slog.String("LogLevel", c.LogLevel.String()),
		slog.String("LogFormat", c.LogFormat),
	)
}

// Load builds a Config from getenv. If SANDPOST_CONFIG_FILE is set, the file
// provides defaults that environment variables override. All validation
// problems are reported together.
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	fileVals := map[string]string{}
	if path := strings.TrimSpace(getenv(configFileVar)); path != "" {
		parsed, err := readFile(path)
		if err != nil {
			return Config{}, err
		}
		fileVals = parsed
	}
	get := func(key string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return strings.TrimSpace(fileVals[key])
	}
	return build(get)
}

func build(get func(string) string) (Config, error) {
	var errs []error
	c := Config{
		DatabaseURL:     get("DATABASE_URL"),
		SecretKey:       get("SECRET_KEY"),
		SMTPListen:      orDefault(get("SMTP_LISTEN"), DefaultSMTPListen),
		HTTPListen:      orDefault(get("HTTP_LISTEN"), DefaultHTTPListen),
		TLSCertFile:     get("TLS_CERT_FILE"),
		TLSKeyFile:      get("TLS_KEY_FILE"),
		SMTPTLSMode:     strings.ToLower(get("SMTP_TLS_MODE")),
		AdminEmail:      get("ADMIN_EMAIL"),
		AdminPassword:   get("ADMIN_PASSWORD"),
		MaxMessageBytes: DefaultMaxMessageBytes,
		LogLevel:        slog.LevelInfo,
		LogFormat:       orDefault(strings.ToLower(get("LOG_FORMAT")), logFormatText),
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	switch {
	case c.SecretKey == "":
		errs = append(errs, errors.New("SECRET_KEY is required"))
	case len(c.SecretKey) < minSecretKeyBytes:
		errs = append(errs, fmt.Errorf("SECRET_KEY must be at least %d bytes", minSecretKeyBytes))
	}
	for _, kv := range [][2]string{{"SMTP_LISTEN", c.SMTPListen}, {"HTTP_LISTEN", c.HTTPListen}} {
		if _, _, err := net.SplitHostPort(kv[1]); err != nil {
			errs = append(errs, fmt.Errorf("%s is not a valid host:port address: %w", kv[0], err))
		}
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		errs = append(errs, errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together"))
	}
	errs = append(errs, validateAdmin(c)...)

	if c.SMTPTLSMode == "" {
		c.SMTPTLSMode = TLSOff
		if c.TLSCertFile != "" {
			c.SMTPTLSMode = TLSStartTLS
		}
	}
	switch c.SMTPTLSMode {
	case TLSOff:
	case TLSStartTLS, TLSRequired, TLSImplicit:
		if c.TLSCertFile == "" {
			errs = append(errs, fmt.Errorf("SMTP_TLS_MODE=%s needs TLS_CERT_FILE and TLS_KEY_FILE", c.SMTPTLSMode))
		}
	default:
		errs = append(errs, errors.New("SMTP_TLS_MODE must be one of off, starttls, required, implicit"))
	}

	if raw := get("MAX_MESSAGE_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("MAX_MESSAGE_BYTES is not an integer: %w", err))
		case n <= 0:
			errs = append(errs, errors.New("MAX_MESSAGE_BYTES must be positive"))
		default:
			c.MaxMessageBytes = n
		}
	}
	if raw := get("LOG_LEVEL"); raw != "" {
		if err := c.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL is invalid: %w", err))
		}
	}
	if c.LogFormat != logFormatText && c.LogFormat != logFormatJSON {
		errs = append(errs, errors.New("LOG_FORMAT must be one of text, json"))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

func validateAdmin(c Config) []error {
	if c.AdminEmail == "" && c.AdminPassword == "" {
		return nil
	}
	if c.AdminEmail == "" || c.AdminPassword == "" {
		return []error{errors.New("ADMIN_EMAIL and ADMIN_PASSWORD must be set together")}
	}
	var errs []error
	if _, err := mail.ParseAddress(c.AdminEmail); err != nil {
		errs = append(errs, errors.New("ADMIN_EMAIL is not a valid email"))
	}
	if len(c.AdminPassword) < minAdminPasswordLength {
		errs = append(errs, fmt.Errorf("ADMIN_PASSWORD must be at least %d characters", minAdminPasswordLength))
	}
	return errs
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// readFile parses a KEY=VALUE file. Blank lines and lines starting with # are
// ignored, an optional "export " prefix is accepted, and values may be quoted.
func readFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-provided configuration
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("config file %s line %d: expected KEY=VALUE", path, i+1)
		}
		out[key] = unquote(strings.TrimSpace(val))
	}
	return out, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
