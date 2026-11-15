package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"golang.org/x/sync/errgroup"

	"github.com/sandpost-mail/sandpost/internal/activity"
	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/logging"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/secrets"
	"github.com/sandpost-mail/sandpost/internal/smtpd"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
	"github.com/sandpost-mail/sandpost/internal/webui"
)

const (
	readHeaderTimeout = 10 * time.Second
	// idleTimeout closes keep-alive connections that carry no request. There
	// is deliberately no ReadTimeout or WriteTimeout: they would also end SSE
	// streams and the long poll wait (up to 120s).
	idleTimeout    = 120 * time.Second
	maxHeaderBytes = 1 << 20
	// shutdownTimeout bounds the drain of in-flight HTTP requests.
	shutdownTimeout = 15 * time.Second
	// jobStopTimeout must exceed River's SoftStopTimeout, after which River
	// cancels the running jobs itself, so they can record their state.
	jobStopTimeout = 20 * time.Second

	// streamPathPrefix and streamSuffixes identify the long lived routes (SSE
	// and long poll wait) that shutdown ends instead of waiting for.
	streamPathPrefix = "/_/api/v1/projects/"
)

var streamSuffixes = []string{"/events", "/wait"}

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usageServe = `Usage: sandpost serve

Run the HTTP and SMTP servers. Configuration comes from the environment (and
$SANDPOST_CONFIG_FILE). The database schema must be current: run "sandpost migrate up"
first, serve refuses to start otherwise.
`

// serveArgs handles serve's command line: only -h is accepted.
func serveArgs(e env, args []string) error {
	switch {
	case len(args) == 0:
		return nil
	case len(args) == 1 && isHelp(args[0]):
		_, _ = fmt.Fprint(e.stdout, usageServe)
		return errHelpShown
	default:
		return usageError("unexpected argument %q\n\n%s", args[0], usageServe)
	}
}

func runServe(ctx context.Context, e env) error {
	cfg, err := config.Load(e.getenv)
	if err != nil {
		return err
	}
	log := logging.New(e.stderr, cfg.LogLevel, cfg.LogFormat)
	log.Info("starting", "version", version, "config", cfg)

	// SECRET_KEY stops here: every component gets only its purpose key.
	keys, err := secrets.DeriveKeys(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("derive keys: %w", err)
	}
	box := secrets.NewBox(keys.Transport)

	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Serving on a stale schema only fails later, request by request.
	if err := store.SchemaCurrent(ctx, pool); err != nil {
		return fmt.Errorf("check database schema: %w", err)
	}

	if _, err := users.BootstrapAdmin(ctx, pool, cfg.AdminEmail, cfg.AdminPassword, log); err != nil {
		return err
	}

	// Delivery: the job queue, its worker and the relay client. Captured mail
	// is only ever sent by the worker, after the policy engine allowed it.
	smtpCreds := credentials.New(pool, keys.SMTPCredential)
	relay := &delivery.SMTPRelay{DenyCIDRs: cfg.RelayDenyCIDRs}
	jobs, err := delivery.NewRiverClient(pool, delivery.NewWorker(pool, box, relay, log), log)
	if err != nil {
		return err
	}
	queue := delivery.NewRiverQueue(jobs)

	smtpSrv, err := newSMTPServer(cfg, smtpCreds, ingest.NewService(pool).WithHook(delivery.NewEnqueuer(queue), log), log)
	if err != nil {
		return err
	}
	bus := events.NewBus(cfg.DatabaseURL, log)
	authSvc, err := auth.NewService(pool, keys.Session, keys.CSRF)
	if err != nil {
		return err
	}

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
	info, err := serverInfo(cfg, smtpLn.Addr())
	if err != nil {
		_, _ = httpLn.Close(), smtpLn.Close()
		return err
	}

	handler := api.NewHandler(api.Deps{
		DB:             pool,
		Authz:          authz.New(pool),
		Auth:           authSvc,
		Tokens:         tokens.NewService(pool, keys.APIToken),
		Users:          users.NewService(pool),
		Groups:         groups.NewService(pool),
		Audit:          audit.NewReader(pool),
		Signer:         authz.NewSigner(keys.SignedURL),
		Projects:       projects.New(pool),
		Credentials:    smtpCreds,
		Messages:       messages.New(pool),
		Comments:       comments.New(pool),
		Activity:       activity.NewReader(pool),
		Delivery:       delivery.NewService(pool, box, relay, queue),
		Bus:            bus,
		Info:           info,
		CookieSecure:   cfg.CookieSecure,
		TrustedProxies: cfg.TrustedProxies,
		UI:             webui.Handler(),
		Log:            log,
	})
	if e.ready != nil {
		e.ready(httpLn.Addr(), smtpLn.Addr())
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return runJobs(gctx, jobs, log) })
	g.Go(func() error { return bus.Run(gctx) })
	g.Go(func() error { return smtpSrv.Serve(gctx, smtpLn) })
	g.Go(func() error { return serveHTTP(gctx, httpLn, handler, log) })
	return g.Wait()
}

func newSMTPServer(cfg config.Config, creds *credentials.Service, ing *ingest.Service, log *slog.Logger) (*smtpd.Server, error) {
	opts := smtpd.Options{
		Verifier:        creds,
		Ingester:        ing,
		Log:             log,
		MaxMessageBytes: cfg.MaxMessageBytes,
		TLSMode:         cfg.SMTPTLSMode,
		MaxConnections:  cfg.SMTPMaxConnections,
	}
	if cfg.SMTPTLSMode != smtpd.TLSOff {
		tlsCfg, err := smtpd.LoadTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		opts.TLSConfig = tlsCfg
	}
	return smtpd.New(opts)
}

// serverInfo derives the public connection hints shown in the UI from the
// bound SMTP address, so a named or zero port is reported as really bound.
func serverInfo(cfg config.Config, smtpAddr net.Addr) (api.ServerInfo, error) {
	tcp, ok := smtpAddr.(*net.TCPAddr)
	if !ok || tcp.Port <= 0 {
		return api.ServerInfo{}, fmt.Errorf("smtp listener %q has no TCP port", smtpAddr)
	}
	return api.ServerInfo{SMTPPort: tcp.Port, SMTPTLSMode: cfg.SMTPTLSMode, Version: version}, nil
}

// isStream reports whether r is a long lived request (SSE events or the long
// poll wait) that shutdown ends instead of waiting for.
func isStream(r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, streamPathPrefix) {
		return false
	}
	for _, s := range streamSuffixes {
		if strings.HasSuffix(r.URL.Path, s) {
			return true
		}
	}
	return false
}

// endStreamsOn gives stream requests a context that is also cancelled when
// streams is done. Other requests keep their own context, so shutdown lets
// them finish.
func endStreamsOn(streams context.Context, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStream(r) {
			h.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(streams, cancel)
		defer stop()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// serveHTTP serves h on ln until ctx is cancelled, then shuts down gracefully:
// long lived streams end at once, other in-flight requests get up to
// shutdownTimeout to finish before their connections are closed.
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	return serveHTTPWithGrace(ctx, ln, h, log, shutdownTimeout)
}

func serveHTTPWithGrace(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger, grace time.Duration) error {
	// Requests do not inherit ctx: the signal must not abort them mid-way.
	// reqCtx is only cancelled when the grace period is over.
	reqCtx, cancelReqs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelReqs()
	streams, endStreams := context.WithCancel(reqCtx)
	defer endStreams()
	srv := &http.Server{
		Handler:           endStreamsOn(streams, h),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(endStreams)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	log.Info("http listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down, send the signal again to force exit")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("http shutdown incomplete, closing remaining connections", "error", err)
		cancelReqs()
		_ = srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// jobQueue is the part of the River client runJobs drives.
type jobQueue interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

var _ jobQueue = (*river.Client[pgx.Tx])(nil)

// runJobs runs the delivery workers until ctx is cancelled, then lets running
// jobs finish. River cancels jobs still running after its SoftStopTimeout;
// a Stop that still exceeds jobStopTimeout is logged, not fatal, so an
// ordinary SIGTERM exits 0.
func runJobs(ctx context.Context, c jobQueue, log *slog.Logger) error {
	return runJobsWithBudget(ctx, c, log, jobStopTimeout)
}

func runJobsWithBudget(ctx context.Context, c jobQueue, log *slog.Logger, budget time.Duration) error {
	// The client outlives ctx so that Stop can finish running jobs.
	if err := c.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start job queue: %w", err)
	}
	log.Info("job queue started")
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	if err := c.Stop(sctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Warn("job queue did not stop in time, running jobs are left to River's rescuer",
				"budget", budget, "error", err)
			return nil
		}
		return fmt.Errorf("stop job queue: %w", err)
	}
	return nil
}
