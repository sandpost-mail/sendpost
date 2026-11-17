// Package config loads Sandpost configuration from environment variables and
// an optional KEY=VALUE file. Environment variables always win over the file.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"strconv"
	"strings"
)

// Defaults for optional settings.
const (
	DefaultSMTPListen               = ":1025"
	DefaultHTTPListen               = ":8080"
	DefaultMaxMessageBytes    int64 = 25 << 20
	DefaultSMTPMaxConnections       = 100
	minSecretKeyBytes               = 32
	// minSecretKeyDistinct rejects obviously weak keys such as one repeated
	// character. Any generated key (hex, base64) passes easily.
	minSecretKeyDistinct   = 8
	minAdminPasswordLength = 12
	configFileVar          = "SANDPOST_CONFIG_FILE"
	logFormatText          = "text"
	logFormatJSON          = "json"
)

// SMTP TLS modes (SMTP_TLS_MODE).
const (
	// TLSOff serves plaintext SMTP only.
	TLSOff = "off"
	// TLSStartTLS offers STARTTLS and refuses AUTH until it is active.
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
	// CookieSecure marks the UI session cookie Secure. It defaults to true
	// (browsers also accept Secure cookies on http://localhost). Set
	// COOKIE_SECURE=false only when users reach the UI over plain http on a
	// name other than localhost, for example a LAN address.
	CookieSecure bool
	LogLevel     slog.Level
	LogFormat    string
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header is
	// believed when deriving the client address (TRUSTED_PROXIES). Empty
	// trusts nobody.
	TrustedProxies []netip.Prefix
	// SMTPMaxConnections caps concurrent SMTP connections (SMTP_MAX_CONNECTIONS).
	SMTPMaxConnections int
	// RelayDenyCIDRs are networks the delivery relay must never connect to
	// (RELAY_DENY_CIDRS). Empty denies nothing.
	RelayDenyCIDRs []netip.Prefix
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
		slog.Bool("CookieSecure", c.CookieSecure),
		slog.String("LogLevel", c.LogLevel.String()),
		slog.String("LogFormat", c.LogFormat),
		slog.Int("TrustedProxies", len(c.TrustedProxies)),
		slog.Int("SMTPMaxConnections", c.SMTPMaxConnections),
		slog.Int("RelayDenyCIDRs", len(c.RelayDenyCIDRs)),
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
		CookieSecure:    true,
		LogLevel:        slog.LevelInfo,
		LogFormat:       orDefault(strings.ToLower(get("LOG_FORMAT")), logFormatText),

		SMTPMaxConnections: DefaultSMTPMaxConnections,
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	switch {
	case c.SecretKey == "":
		errs = append(errs, errors.New("SECRET_KEY is required"))
	case len(c.SecretKey) < minSecretKeyBytes:
		errs = append(errs, fmt.Errorf("SECRET_KEY must be at least %d bytes", minSecretKeyBytes))
	case distinctBytes(c.SecretKey) < minSecretKeyDistinct:
		errs = append(errs, fmt.Errorf(
			"SECRET_KEY is too repetitive (fewer than %d distinct characters), generate one with: openssl rand -hex 32",
			minSecretKeyDistinct))
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
	if raw := get("COOKIE_SECURE"); raw != "" {
		secure, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("COOKIE_SECURE must be true or false"))
		}
		c.CookieSecure = secure
	}
	if raw := get("LOG_LEVEL"); raw != "" {
		if err := c.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL is invalid: %w", err))
		}
	}
	if c.LogFormat != logFormatText && c.LogFormat != logFormatJSON {
		errs = append(errs, errors.New("LOG_FORMAT must be one of text, json"))
	}
	if raw := get("SMTP_MAX_CONNECTIONS"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("SMTP_MAX_CONNECTIONS is not an integer: %w", err))
		case n <= 0:
			errs = append(errs, errors.New("SMTP_MAX_CONNECTIONS must be positive"))
		default:
			c.SMTPMaxConnections = n
		}
	}
	var err error
	if c.TrustedProxies, err = parseCIDRs("TRUSTED_PROXIES", get("TRUSTED_PROXIES")); err != nil {
		errs = append(errs, err)
	}
	if c.RelayDenyCIDRs, err = parseCIDRs("RELAY_DENY_CIDRS", get("RELAY_DENY_CIDRS")); err != nil {
		errs = append(errs, err)
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

// parseCIDRs parses a comma separated list of CIDR prefixes. A bare address
// is accepted as a single host prefix (/32 or /128).
func parseCIDRs(name, raw string) ([]netip.Prefix, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(item)
		if err != nil || a.Zone() != "" {
			return nil, fmt.Errorf("%s: %q is not a CIDR prefix or IP address", name, item)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func distinctBytes(s string) int {
	var seen [256]bool
	n := 0
	for i := 0; i < len(s); i++ {
		if !seen[s[i]] {
			seen[s[i]] = true
			n++
		}
	}
	return n
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
