package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/policy"
)

// TLS modes of a transport.
const (
	// TLSStartTLS upgrades the connection with STARTTLS and fails when the
	// relay does not offer it. The certificate is always verified.
	TLSStartTLS = "starttls"
	// TLSImplicit wraps the connection in TLS from the start (port 465).
	TLSImplicit = "implicit"
	// TLSNone is plaintext. It can not be combined with a password.
	TLSNone = "none"
)

const (
	dialTimeout       = 15 * time.Second
	commandTimeout    = 30 * time.Second
	submissionTimeout = 2 * time.Minute
	maxErrorRunes     = 300
	localName         = "sandpost.local"
)

// RelayConfig is everything needed to reach a relay. It carries a password,
// so it never formats itself with it.
type RelayConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	TLSMode  string
}

// String implements fmt.Stringer without the credentials.
func (c RelayConfig) String() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// GoString keeps %#v from printing the password.
func (c RelayConfig) GoString() string { return "delivery.RelayConfig{" + c.String() + "}" }

// LogValue implements slog.LogValuer without the credentials.
func (c RelayConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.String("addr", c.String()), slog.String("tls", c.TLSMode))
}

// Relay stages that can fail.
const (
	stageConnect = "connect"
	stageTLS     = "tls"
	stageAuth    = "auth"
	stageMail    = "mail"
	stageRcpt    = "rcpt"
	stageData    = "data"
)

// RelayError is a failed step. Permanent errors are not retried.
type RelayError struct {
	Stage     string
	Code      int // SMTP reply code, 0 when there was none
	Text      string
	Permanent bool
	// public is a fixed phrase that carries nothing the relay said.
	public string
}

func (e *RelayError) Error() string { return e.Stage + ": " + e.Text }

// Public describes the failure without any text supplied by the remote host,
// for the connection test: it must not turn Sandpost into a way to read
// banners of arbitrary hosts and ports.
func (e *RelayError) Public() string {
	s := e.Stage + " failed"
	if e.public != "" {
		s += ": " + e.public
	}
	if e.Code != 0 {
		s += " (" + strconv.Itoa(e.Code) + ")"
	}
	return s
}

// RcptResult is the outcome for one envelope recipient. Err is nil when the
// relay accepted the message for it.
type RcptResult struct {
	Address string
	Err     *RelayError
}

// Accepted reports whether the relay took the message for this recipient.
func (r RcptResult) Accepted() bool { return r.Err == nil }

// CheckResult is the outcome of a connection test.
type CheckResult struct {
	OK            bool   `json:"ok"`
	TLS           bool   `json:"tls"`
	Authenticated bool   `json:"authenticated"`
	Error         string `json:"error,omitempty"`
}

// Relay sends vetted outbound messages. Send takes a policy.Outbound, a type
// only the policy engine can build, so nothing that did not pass the engine
// can reach the wire.
type Relay interface {
	// Send delivers ob and returns one result per envelope recipient, in order.
	Send(ctx context.Context, cfg RelayConfig, ob policy.Outbound) []RcptResult
	// Check connects, secures the connection and authenticates, without
	// submitting any mail.
	Check(ctx context.Context, cfg RelayConfig) CheckResult
}

// SMTPRelay is the Relay that speaks SMTP.
type SMTPRelay struct {
	// TLSConfig is the base TLS configuration, nil for the system roots. The
	// server name and a TLS 1.2 minimum are always set, certificates are
	// always verified. Tests use it to trust their own authority.
	TLSConfig *tls.Config
}

var _ Relay = (*SMTPRelay)(nil)

func (r *SMTPRelay) tlsConfig(host string) *tls.Config {
	var c *tls.Config
	if r.TLSConfig != nil {
		c = r.TLSConfig.Clone()
	} else {
		c = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	c.ServerName = host
	c.InsecureSkipVerify = false
	if c.MinVersion < tls.VersionTLS12 {
		c.MinVersion = tls.VersionTLS12
	}
	return c
}

// Send implements Relay.
func (r *SMTPRelay) Send(ctx context.Context, cfg RelayConfig, ob policy.Outbound) []RcptResult {
	rcpts := ob.Recipients()
	results := make([]RcptResult, len(rcpts))
	for i, a := range rcpts {
		results[i].Address = a
	}
	accepted := make([]bool, len(rcpts))
	failAll := func(e *RelayError) []RcptResult {
		for i := range results {
			if !accepted[i] && results[i].Err == nil {
				results[i].Err = e
			}
		}
		return results
	}
	if len(rcpts) == 0 {
		return failAll(&RelayError{Stage: stageRcpt, Text: "no recipients", Permanent: true})
	}

	c, stop, rerr := r.open(ctx, cfg)
	if rerr != nil {
		return failAll(rerr)
	}
	defer stop()
	defer func() { _ = c.Close() }()

	if err := c.Mail(ob.Sender(), nil); err != nil {
		return failAll(classify(stageMail, err, cfg.Password))
	}
	count := 0
	for i, to := range rcpts {
		if err := c.Rcpt(to, nil); err != nil {
			results[i].Err = classify(stageRcpt, err, cfg.Password)
			continue
		}
		accepted[i] = true
		count++
	}
	if count == 0 {
		return results
	}
	if e := submit(c, ob.Data(), cfg.Password); e != nil {
		for i := range results {
			if accepted[i] {
				results[i].Err = e
			}
		}
		return results
	}
	_ = c.Quit()
	return results
}

func submit(c *smtp.Client, data []byte, password string) *RelayError {
	w, err := c.Data()
	if err != nil {
		return classify(stageData, err, password)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return classify(stageData, err, password)
	}
	if err := w.Close(); err != nil {
		return classify(stageData, err, password)
	}
	return nil
}

// Check implements Relay.
func (r *SMTPRelay) Check(ctx context.Context, cfg RelayConfig) CheckResult {
	c, stop, rerr := r.open(ctx, cfg)
	if rerr != nil {
		return CheckResult{Error: rerr.Public()}
	}
	defer stop()
	defer func() { _ = c.Close() }()
	res := CheckResult{OK: true, TLS: cfg.TLSMode != TLSNone, Authenticated: cfg.Username != ""}
	if err := c.Noop(); err != nil {
		e := classify(stageConnect, err, cfg.Password)
		return CheckResult{Error: e.Public()}
	}
	_ = c.Quit()
	return res
}

// open connects, secures and authenticates. stop must be called when done.
func (r *SMTPRelay) open(ctx context.Context, cfg RelayConfig) (*smtp.Client, func(), *RelayError) {
	if cfg.TLSMode == TLSNone && (cfg.Username != "" || cfg.Password != "") {
		return nil, nil, &RelayError{Stage: stageTLS, Permanent: true,
			Text: "refusing to send credentials without TLS", public: "credentials are not sent without TLS"}
	}
	if cfg.TLSMode != TLSNone && cfg.TLSMode != TLSStartTLS && cfg.TLSMode != TLSImplicit {
		return nil, nil, &RelayError{Stage: stageTLS, Permanent: true, Text: "unknown TLS mode", public: "unknown TLS mode"}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: dialTimeout}

	var conn net.Conn
	var err error
	if cfg.TLSMode == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: r.tlsConfig(cfg.Host)}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		stage := stageConnect
		if cfg.TLSMode == TLSImplicit && isTLSError(err) {
			stage = stageTLS
		}
		return nil, nil, classify(stage, err, cfg.Password)
	}
	// Closing the connection is how a cancelled context interrupts a blocked read.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })

	var c *smtp.Client
	if cfg.TLSMode == TLSStartTLS {
		c, err = smtp.NewClientStartTLS(conn, r.tlsConfig(cfg.Host))
		if err != nil {
			stop()
			return nil, nil, classify(stageTLS, err, cfg.Password)
		}
	} else {
		c = smtp.NewClient(conn)
	}
	c.CommandTimeout, c.SubmissionTimeout = commandTimeout, submissionTimeout
	if err := c.Hello(localName); err != nil {
		stop()
		_ = c.Close()
		return nil, nil, classify(stageConnect, err, cfg.Password)
	}
	if cfg.Username != "" {
		if e := authenticate(c, cfg); e != nil {
			stop()
			_ = c.Close()
			return nil, nil, e
		}
	}
	return c, func() { stop() }, nil
}

func authenticate(c *smtp.Client, cfg RelayConfig) *RelayError {
	_, mechs := c.Extension("AUTH")
	var client sasl.Client
	switch fields := strings.Fields(strings.ToUpper(mechs)); {
	case contains(fields, "PLAIN"):
		client = sasl.NewPlainClient("", cfg.Username, cfg.Password)
	case contains(fields, "LOGIN"):
		client = sasl.NewLoginClient(cfg.Username, cfg.Password)
	default:
		return &RelayError{Stage: stageAuth, Permanent: true, Text: "the relay offers no usable AUTH mechanism",
			public: "the relay offers no usable AUTH mechanism"}
	}
	if err := c.Auth(client); err != nil {
		return classify(stageAuth, err, cfg.Password)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func isTLSError(err error) bool {
	var (
		cve *tls.CertificateVerificationError
		rh  tls.RecordHeaderError
		ua  x509.UnknownAuthorityError
		hn  x509.HostnameError
		ci  x509.CertificateInvalidError
	)
	return errors.As(err, &cve) || errors.As(err, &rh) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci)
}

// classify turns an error of a step into a RelayError. SMTP 5xx replies and
// TLS or configuration problems are permanent, everything else (4xx, network
// trouble, timeouts) may succeed later.
func classify(stage string, err error, password string) *RelayError {
	var se *smtp.SMTPError
	switch {
	case errors.As(err, &se):
		return &RelayError{
			Stage: stage, Code: se.Code, Permanent: se.Code >= 500,
			Text: cleanText(fmt.Sprintf("%d %s", se.Code, se.Message), password),
		}
	case isTLSError(err):
		return &RelayError{Stage: stageTLS, Permanent: true, Text: cleanText(err.Error(), password), public: "certificate verification failed"}
	case strings.Contains(err.Error(), "doesn't support STARTTLS"):
		return &RelayError{Stage: stageTLS, Permanent: true, Text: "the relay does not offer STARTTLS", public: "the relay does not offer STARTTLS"}
	case stage == stageTLS:
		return &RelayError{Stage: stage, Text: cleanText(err.Error(), password), public: "handshake failed"}
	}
	re := &RelayError{Stage: stage, Text: cleanText(err.Error(), password)}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		re.public = "timed out"
	case errors.Is(err, context.Canceled):
		re.public = "cancelled"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		re.public = "the connection was closed"
	}
	return re
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// cleanText makes remote or network text safe to store and show: control
// characters are dropped, the password is masked if the remote echoed it, and
// the length is bounded.
func cleanText(s, password string) string {
	if password != "" {
		s = strings.ReplaceAll(s, password, "[redacted]")
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if n++; n >= maxErrorRunes {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
