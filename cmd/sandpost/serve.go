package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/logging"
	"github.com/sandpost-mail/sandpost/internal/store"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func runServe(ctx context.Context, e env) error {
	cfg, err := config.Load(e.getenv)
	if err != nil {
		return err
	}
	log := logging.New(e.stderr, cfg.LogLevel, cfg.LogFormat)
	log.Info("starting", "config", cfg)

	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPListen)
	if err != nil {
		return fmt.Errorf("listen http: %w", err)
	}
	return serveHTTP(ctx, ln, api.NewHandler(pool), log)
}

// serveHTTP serves h on ln until ctx is cancelled, then shuts down gracefully.
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	log.Info("http listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
