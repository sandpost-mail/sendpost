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

	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/ingest"
)

const (
	maxRecipients = 100
	// Lenient on purpose: a sandbox should capture sloppy mailers that emit
	// very long unwrapped lines. The total size is still capped.
	maxLineLength  = 1 << 20
	ioTimeout      = 5 * time.Minute
	ingestTimeout  = 30 * time.Second
	shutdownGrace  = 10 * time.Second
	defaultHelloAs = "sandpost"
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
	Verifier        Verifier
	Ingester        Ingester
	Log             *slog.Logger
	MaxMessageBytes int64
	// TLSMode is one of config.TLSOff, TLSStartTLS, TLSRequired, TLSImplicit.
	TLSMode string
	// TLSConfig is required for every TLSMode except config.TLSOff.
	TLSConfig *tls.Config
	// Limiter throttles failed logins per IP. A default one is used when nil.
	Limiter *FailureLimiter
	// Domain is announced in the greeting.
	Domain string
}

// Server wraps a go-smtp server.
type Server struct {
	srv      *smtp.Server
	implicit bool
	tlsCfg   *tls.Config
	log      *slog.Logger
}

// New builds a Server, it does not listen yet.
func New(opts Options) (*Server, error) {
	if opts.Verifier == nil || opts.Ingester == nil {
		return nil, errors.New("smtpd: verifier and ingester are required")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Limiter == nil {
		opts.Limiter = NewFailureLimiter(DefaultMaxAuthFailures, DefaultAuthFailWindow)
	}
	if opts.Domain == "" {
		opts.Domain = defaultHelloAs
	}
	if opts.TLSMode == "" {
		opts.TLSMode = config.TLSOff
	}
	if opts.TLSMode != config.TLSOff && opts.TLSConfig == nil {
		return nil, fmt.Errorf("smtpd: TLS mode %q needs a TLS config", opts.TLSMode)
	}

	be := &backend{verifier: opts.Verifier, ingester: opts.Ingester, limiter: opts.Limiter, log: opts.Log}
	srv := smtp.NewServer(be)
	srv.Domain = opts.Domain
	srv.MaxMessageBytes = opts.MaxMessageBytes
	srv.MaxRecipients = maxRecipients
	srv.MaxLineLength = maxLineLength
	srv.ReadTimeout = ioTimeout
	srv.WriteTimeout = ioTimeout
	srv.EnableSMTPUTF8 = true
	srv.ErrorLog = slogAdapter{opts.Log}
	srv.AllowInsecureAuth = opts.TLSMode == config.TLSOff || opts.TLSMode == config.TLSStartTLS
	if opts.TLSMode != config.TLSOff {
		srv.TLSConfig = opts.TLSConfig
	}
	return &Server{
		srv: srv, log: opts.Log,
		implicit: opts.TLSMode == config.TLSImplicit, tlsCfg: opts.TLSConfig,
	}, nil
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully. It returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.implicit {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(ln) }()
	s.log.Info("smtp listening", "addr", ln.Addr().String(), "implicit_tls", s.implicit)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			return fmt.Errorf("smtp server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := s.srv.Shutdown(sctx); err != nil {
		// Grace period over: drop the remaining connections.
		_ = s.srv.Close()
	}
	<-errCh
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
