package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/logging"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/smtpd"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
	"github.com/sandpost-mail/sandpost/internal/webui"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func runServe(ctx context.Context, e env) error {
	cfg, err := config.Load(e.getenv)
	if err != nil {
		return err
	}
	log := logging.New(e.stderr, cfg.LogLevel, cfg.LogFormat)
	log.Info("starting", "version", version, "config", cfg)

	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := users.BootstrapAdmin(ctx, pool, cfg.AdminEmail, cfg.AdminPassword, log); err != nil {
		return err
	}

	smtpSrv, err := newSMTPServer(cfg, pool, log)
	if err != nil {
		return err
	}
	bus := events.NewBus(cfg.DatabaseURL, log)
	authSvc, err := auth.NewService(pool, cfg.SecretKey)
	if err != nil {
		return err
	}
	handler := api.NewHandler(api.Deps{
		DB:           pool,
		Authz:        authz.New(pool),
		Auth:         authSvc,
		Tokens:       tokens.NewService(pool, cfg.SecretKey),
		Users:        users.NewService(pool),
		Groups:       groups.NewService(pool),
		Audit:        audit.NewReader(pool),
		Signer:       authz.NewSigner(cfg.SecretKey),
		Projects:     projects.New(pool),
		Credentials:  credentials.New(pool, cfg.SecretKey),
		Messages:     messages.New(pool),
		Bus:          bus,
		Info:         serverInfo(cfg),
		CookieSecure: cfg.CookieSecure,
		UI:           webui.Handler(),
		Log:          log,
	})

	lc := &net.ListenConfig{}
	httpLn, err := lc.Listen(ctx, "tcp", cfg.HTTPListen)
	if err != nil {
		return fmt.Errorf("listen http: %w", err)
	}
	smtpLn, err := lc.Listen(ctx, "tcp", cfg.SMTPListen)
	if err != nil {
		_ = httpLn.Close()
		return fmt.Errorf("listen smtp: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return bus.Run(gctx) })
	g.Go(func() error { return smtpSrv.Serve(gctx, smtpLn) })
	g.Go(func() error { return serveHTTP(gctx, httpLn, handler, log) })
	return g.Wait()
}

func newSMTPServer(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*smtpd.Server, error) {
	opts := smtpd.Options{
		Verifier:        credentials.New(pool, cfg.SecretKey),
		Ingester:        ingest.NewService(pool),
		Log:             log,
		MaxMessageBytes: cfg.MaxMessageBytes,
		TLSMode:         cfg.SMTPTLSMode,
	}
	if cfg.SMTPTLSMode != config.TLSOff {
		tlsCfg, err := smtpd.LoadTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		opts.TLSConfig = tlsCfg
	}
	return smtpd.New(opts)
}

// serverInfo derives the public connection hints shown in the UI.
func serverInfo(cfg config.Config) api.ServerInfo {
	_, portStr, _ := net.SplitHostPort(cfg.SMTPListen)
	port, _ := strconv.Atoi(portStr)
	return api.ServerInfo{SMTPPort: port, SMTPTLSMode: cfg.SMTPTLSMode, Version: version}
}

// serveHTTP serves h on ln until ctx is cancelled, then shuts down gracefully.
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	// Requests get their own context so shutdown can end long lived SSE streams
	// instead of waiting for them.
	reqCtx, cancelReqs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelReqs()
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	log.Info("http listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	cancelReqs()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
