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
	"net/netip"
	"strconv"
	"strings"
	"syscall"
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
	// checkTimeout bounds a whole connection test (dial, greeting, TLS, AUTH).
	checkTimeout  = 45 * time.Second
	maxErrorRunes = 300
	localName     = "sandpost.local"
)

// Fixed phrases of the connection test.
const (
	// publicConnect is the only thing a failed connect stage reports: refused,
	// filtered, closed and non-SMTP ports look the same, so the test is not a
	// port scanner.
	publicConnect = "could not reach an SMTP relay at this address"
	publicDenied  = "the relay address is not allowed on this server"
	// unknownOutcome prefixes a failure after the final dot: the relay may
	// have queued the message, a retry can deliver it twice.
	unknownOutcome = "outcome unknown after end of data: "
)

// errDeniedTarget is returned by the dialer for an address in DenyCIDRs.
var errDeniedTarget = errors.New("the relay address is in a denied range")

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
// banners of arbitrary hosts and ports, nor tell refused, filtered and open
// ports apart. Every connect stage failure reads the same.
func (e *RelayError) Public() string {
	if e.Stage == stageConnect {
		if e.public == publicDenied {
			return "connect failed: " + publicDenied
		}
		return "connect failed: " + publicConnect
	}
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
	// DenyCIDRs lists address ranges the relay must never connect to (for
	// example 127.0.0.0/8, 169.254.0.0/16 or the cluster network). It is
	// checked on the resolved address of every connection, so a host name
	// that resolves (or is rebound) into a denied range is refused too.
	// Empty allows every address: relays on private networks are the
	// common case.
	DenyCIDRs []netip.Prefix
}

// control is the net.Dialer Control hook enforcing DenyCIDRs.
func (r *SMTPRelay) control(_, address string, _ syscall.RawConn) error {
	if len(r.DenyCIDRs) == 0 {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", errDeniedTarget, address) // fail closed
	}
	// A zoned address never matches a prefix, and an IPv4 address and its
	// IPv4-mapped IPv6 form must match the same ranges: check the plain and
	// the 16-byte form, without zone.
	ip := ap.Addr().WithZone("").Unmap()
	mapped := netip.AddrFrom16(ip.As16())
	for _, p := range r.DenyCIDRs {
		if p.Contains(ip) || p.Contains(mapped) {
			return errDeniedTarget
		}
	}
	return nil
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
	if e := submit(c, ob, cfg.Password); e != nil {
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

func submit(c *smtp.Client, ob policy.Outbound, password string) *RelayError {
	w, err := c.Data()
	if err != nil {
		return classify(stageData, err, password)
	}
	if _, err := ob.WriteTo(w); err != nil {
		_ = w.Close()
		return classify(stageData, err, password)
	}
	if err := w.Close(); err != nil {
		return endOfData(err, password)
	}
	return nil
}

// endOfData classifies a failure after the final dot. A reply from the relay
// is classified as usual. Anything else (connection lost, read timeout)
// leaves the outcome unknown: the relay may have queued the message. It stays
// transient, as for any MTA (RFC 5321 4.5.4.1, RFC 1047), so a blip does not
// lose mail, but the text says a retry may deliver it twice.
func endOfData(err error, password string) *RelayError {
	re := classify(stageData, err, password)
	var se *smtp.SMTPError
	if !errors.As(err, &se) && !re.Permanent {
		re.Text = cleanText(unknownOutcome+re.Text, password)
	}
	return re
}

// Check implements Relay. The whole test is bounded by checkTimeout.
func (r *SMTPRelay) Check(ctx context.Context, cfg RelayConfig) CheckResult {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
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
	dialer := &net.Dialer{Timeout: dialTimeout, Control: r.control}

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

	if cfg.TLSMode == TLSStartTLS {
		tc, rerr := startTLS(ctx, conn, r.tlsConfig(cfg.Host), cfg.Password)
		if rerr != nil {
			stop()
			_ = conn.Close()
			return nil, nil, rerr
		}
		conn = tc
	}
	c := smtp.NewClient(conn)
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
	case errors.Is(err, errDeniedTarget):
		return &RelayError{Stage: stageConnect, Permanent: true, Text: errDeniedTarget.Error(), public: publicDenied}
	case isTLSError(err):
		return &RelayError{Stage: stageTLS, Permanent: true, Text: cleanText(err.Error(), password), public: "certificate verification failed"}
	case errors.Is(err, errNoStartTLS):
		return &RelayError{Stage: stageTLS, Permanent: true, Text: errNoStartTLS.Error(), public: errNoStartTLS.Error()}
	case errors.Is(err, errDataAfterStartTLS):
		return &RelayError{Stage: stageTLS, Permanent: true, Text: errDataAfterStartTLS.Error(), public: "handshake failed"}
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
