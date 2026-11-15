// Package smtpd is the SMTP listener. It only accepts authenticated
// submissions: the credential fixes the project, and nothing in the message can
// change which project receives it.
package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/emersion/go-smtp"
	"golang.org/x/net/netutil"
	"golang.org/x/sync/semaphore"

	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/ratelimit"
)

const (
	maxRecipients = 100
	// Lenient on purpose: a sandbox should capture sloppy mailers that emit
	// very long unwrapped lines. The total size is still capped.
	maxLineLength = 1 << 20
	ioTimeout     = 5 * time.Minute
	ingestTimeout = 30 * time.Second
	authTimeout   = 5 * time.Second
	// dataWaitTimeout bounds how long DATA waits for in-flight memory before
	// answering 451.
	dataWaitTimeout = 10 * time.Second
	// shutdownGrace lets in-flight sessions finish. When it ends, pending
	// database work is cancelled (and rolled back) before the connections are
	// dropped, so a client never loses a 250 for a message that was stored.
	shutdownGrace  = 10 * time.Second
	defaultHelloAs = "sandpost"

	// DefaultMaxConnections caps concurrent SMTP connections.
	DefaultMaxConnections = 100
	// DefaultMaxInFlightBytes bounds the memory held by DATA commands being
	// received at once. It is raised to MaxMessageBytes when that is larger.
	DefaultMaxInFlightBytes int64 = 256 << 20
	// DefaultMaxAuthFailures is the number of failed authentications a
	// client may make within DefaultAuthFailWindow.
	DefaultMaxAuthFailures = 10
	// DefaultAuthFailWindow is the window of DefaultMaxAuthFailures.
	DefaultAuthFailWindow = 5 * time.Minute
)

// TLSMode selects how the listener uses TLS. The values match SMTP_TLS_MODE.
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

// Verifier checks SMTP credentials. *credentials.Service implements it.
type Verifier interface {
	Verify(ctx context.Context, username, secret string) (credentials.Identity, error)
}

// Ingester stores a received message. *ingest.Service implements it.
type Ingester interface {
	Ingest(ctx context.Context, in ingest.Input) (int64, error)
}

// Options configures a Server.
type Options struct {
	Verifier Verifier
	Ingester Ingester
	Log      *slog.Logger
	// MaxMessageBytes caps one message, it must be positive.
	MaxMessageBytes int64
	// MaxConnections caps concurrent connections. Zero means DefaultMaxConnections.
	MaxConnections int
	// MaxInFlightBytes bounds the memory of messages being received at once.
	// Zero means DefaultMaxInFlightBytes. It is raised to MaxMessageBytes.
	MaxInFlightBytes int64
	// TLSMode is one of TLSOff, TLSStartTLS, TLSRequired, TLSImplicit.
	// Empty means TLSOff.
	TLSMode string
	// TLSConfig is required for every TLSMode except TLSOff.
	TLSConfig *tls.Config
	// Limiter throttles failed logins per client (IPv4 address or IPv6 /64,
	// see ratelimit.Key). A default one is used when nil.
	Limiter *ratelimit.Limiter
	// Domain is announced in the greeting.
	Domain string
}

// Server wraps a go-smtp server.
type Server struct {
	srv      *smtp.Server
	be       *backend
	implicit bool
	tlsCfg   *tls.Config
	maxConns int
	grace    time.Duration
	log      *slog.Logger
}

// New builds a Server, it does not listen yet.
func New(opts Options) (*Server, error) {
	if err := validate(&opts); err != nil {
		return nil, err
	}
	be := &backend{
		verifier: opts.Verifier, ingester: opts.Ingester, limiter: opts.Limiter, log: opts.Log,
		base: context.Background(), dataMem: semaphore.NewWeighted(opts.MaxInFlightBytes),
		maxMessage: opts.MaxMessageBytes, dataWait: dataWaitTimeout,
	}
	srv := smtp.NewServer(be)
	srv.Domain = opts.Domain
	srv.MaxMessageBytes = opts.MaxMessageBytes
	srv.MaxRecipients = maxRecipients
	srv.MaxLineLength = maxLineLength
	srv.ReadTimeout = ioTimeout
	srv.WriteTimeout = ioTimeout
	srv.EnableSMTPUTF8 = true
	srv.ErrorLog = slogAdapter{opts.Log}
	// AUTH sends the secret in base64: allow it without TLS only when the
	// operator chose to run without TLS at all.
	srv.AllowInsecureAuth = opts.TLSMode == TLSOff
	if opts.TLSMode != TLSOff {
		srv.TLSConfig = opts.TLSConfig
	}
	return &Server{
		srv: srv, be: be, log: opts.Log, maxConns: opts.MaxConnections, grace: shutdownGrace,
		implicit: opts.TLSMode == TLSImplicit, tlsCfg: opts.TLSConfig,
	}, nil
}

// validate checks opts and fills in defaults.
func validate(opts *Options) error {
	if opts.Verifier == nil || opts.Ingester == nil {
		return errors.New("smtpd: verifier and ingester are required")
	}
	if opts.MaxMessageBytes <= 0 {
		return errors.New("smtpd: MaxMessageBytes must be positive")
	}
	if opts.MaxConnections < 0 || opts.MaxInFlightBytes < 0 {
		return errors.New("smtpd: MaxConnections and MaxInFlightBytes must not be negative")
	}
	if opts.MaxConnections == 0 {
		opts.MaxConnections = DefaultMaxConnections
	}
	if opts.MaxInFlightBytes == 0 {
		opts.MaxInFlightBytes = DefaultMaxInFlightBytes
	}
	opts.MaxInFlightBytes = max(opts.MaxInFlightBytes, opts.MaxMessageBytes)
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Limiter == nil {
		opts.Limiter = ratelimit.New(DefaultMaxAuthFailures, DefaultAuthFailWindow)
	}
	if opts.Domain == "" {
		opts.Domain = defaultHelloAs
	}
	switch opts.TLSMode {
	case "":
		opts.TLSMode = TLSOff
	case TLSOff, TLSStartTLS, TLSRequired, TLSImplicit:
	default:
		return fmt.Errorf("smtpd: unknown TLS mode %q", opts.TLSMode)
	}
	if opts.TLSMode != TLSOff && opts.TLSConfig == nil {
		return fmt.Errorf("smtpd: TLS mode %q needs a TLS config", opts.TLSMode)
	}
	return nil
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. It returns nil on a clean shutdown.
//
// Database work done by sessions uses a context that outlives ctx by the
// shutdown grace period and is cancelled when the grace period ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	base, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	s.be.base = base
	ln = netutil.LimitListener(ln, s.maxConns)
	if s.implicit {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(ln) }()
	s.log.Info("smtp listening", "addr", ln.Addr().String(), "implicit_tls", s.implicit,
		"max_connections", s.maxConns)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			return fmt.Errorf("smtp server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.grace)
	defer cancel()
	if err := s.srv.Shutdown(sctx); err != nil {
		// Grace period over: cancel pending database work so it rolls back,
		// then drop the remaining connections.
		stop()
		_ = s.srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, smtp.ErrServerClosed) {
		s.log.Warn("smtp serve ended with an error during shutdown", "error", err)
	}
	return nil
}

// LoadTLS builds a TLS config from a certificate and key file.
func LoadTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls key pair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

type slogAdapter struct{ log *slog.Logger }

func (a slogAdapter) Printf(format string, v ...any) { a.log.Warn(fmt.Sprintf(format, v...)) }
func (a slogAdapter) Println(v ...any)               { a.log.Warn(fmt.Sprint(v...)) }
