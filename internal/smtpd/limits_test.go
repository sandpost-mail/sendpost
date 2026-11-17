package smtpd

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/ratelimit"
)

// serveWith starts srv on a loopback listener. The returned stop cancels the
// serve context and waits for Serve to return.
func serveWith(t *testing.T, srv *Server) (addr string, stop func()) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("serve: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return ln.Addr().String(), stop
}

func newServer(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Verifier == nil {
		opts.Verifier = &fakeVerifier{}
	}
	if opts.Ingester == nil {
		opts.Ingester = &fakeIngester{}
	}
	if opts.MaxMessageBytes == 0 {
		opts.MaxMessageBytes = 1 << 20
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// gatedVerifier blocks every Verify until the gate is closed and records the
// context deadline it was given.
type gatedVerifier struct {
	gate    chan struct{}
	entered chan struct{}

	mu        sync.Mutex
	calls     int
	deadlines []time.Duration
}

func (g *gatedVerifier) Verify(ctx context.Context, u, p string) (credentials.Identity, error) {
	dl, _ := ctx.Deadline()
	g.mu.Lock()
	g.calls++
	g.deadlines = append(g.deadlines, time.Until(dl))
	g.mu.Unlock()
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.gate != nil {
		<-g.gate
	}
	if u == goodUser && p == goodPass {
		return credentials.Identity{CredentialID: 1, ProjectID: projectA}, nil
	}
	return credentials.Identity{}, credentials.ErrInvalid
}

func (g *gatedVerifier) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestParallelAuthFailuresStayWithinBudget: without an atomic reservation
// every parallel attempt would pass the limiter before any failure is recorded.
func TestParallelAuthFailuresStayWithinBudget(t *testing.T) {
	const limit, burst = 3, 8
	v := &gatedVerifier{gate: make(chan struct{})}
	srv := newServer(t, Options{Verifier: v, Limiter: ratelimit.New(limit, time.Minute)})
	addr, _ := serveWith(t, srv)

	results := make(chan int, burst)
	for range burst {
		go func() {
			c, err := dialRaw(addr)
			if err != nil {
				results <- -1
				return
			}
			defer func() { _ = c.Close() }()
			results <- smtpCode(c.Auth(sasl.NewPlainClient("", goodUser, "bad")))
		}()
	}
	// Refused attempts answer without waiting for the gate.
	codes := map[int]int{}
	for range burst - limit {
		codes[<-results]++
	}
	close(v.gate)
	for range limit {
		codes[<-results]++
	}
	if codes[535] != limit || codes[454] != burst-limit {
		t.Errorf("codes = %v, want %d x 535 and %d x 454", codes, limit, burst-limit)
	}
	if n := v.count(); n != limit {
		t.Errorf("verifier called %d times, want %d", n, limit)
	}
}

func TestSuccessfulAuthDoesNotConsumeBudget(t *testing.T) {
	srv := newServer(t, Options{Limiter: ratelimit.New(2, time.Minute)})
	addr, _ := serveWith(t, srv)
	for i := range 5 {
		c := dial(t, addr)
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
}

func TestAuthUsesItsOwnTimeout(t *testing.T) {
	v := &gatedVerifier{}
	srv := newServer(t, Options{Verifier: v})
	addr, _ := serveWith(t, srv)
	c := dial(t, addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.deadlines) != 1 || v.deadlines[0] <= 0 || v.deadlines[0] > authTimeout {
		t.Errorf("auth deadline %v, want within %v", v.deadlines, authTimeout)
	}
}

func TestMaxConnections(t *testing.T) {
	srv := newServer(t, Options{MaxConnections: 1})
	addr, _ := serveWith(t, srv)

	first := dial(t, addr) // holds the only slot
	second, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	r := bufio.NewReader(second)
	_ = second.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("second connection must not be served while the first is open")
	}
	_ = first.Close()
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "220") {
		t.Fatalf("greeting after the slot freed: %q %v", line, err)
	}
}

// blockingIngester holds every Ingest until its context ends or the gate closes.
type blockingIngester struct {
	entered chan struct{}
	gate    chan struct{}
	ctxErr  chan error
}

func (b *blockingIngester) Ingest(ctx context.Context, _ ingest.Input) (int64, error) {
	b.entered <- struct{}{}
	select {
	case <-ctx.Done():
		b.ctxErr <- ctx.Err()
		return 0, ctx.Err()
	case <-b.gate:
		b.ctxErr <- nil
		return 1, nil
	}
}

func TestDataMemoryBudget(t *testing.T) {
	ing := &blockingIngester{entered: make(chan struct{}, 1), gate: make(chan struct{}), ctxErr: make(chan error, 2)}
	srv := newServer(t, Options{Ingester: ing, MaxMessageBytes: 4096, MaxInFlightBytes: 4096})
	srv.be.dataWait = 50 * time.Millisecond
	addr, _ := serveWith(t, srv)

	firstDone := make(chan error, 1)
	go func() {
		c, err := dialRaw(addr)
		if err != nil {
			firstDone <- err
			return
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			firstDone <- err
			return
		}
		firstDone <- c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg))
	}()
	<-ing.entered // the first message holds the whole budget

	c := dial(t, addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg))
	if smtpCode(err) != 451 {
		t.Fatalf("want 451 while the budget is exhausted, got %v", err)
	}
	close(ing.gate)
	if err := <-firstDone; err != nil {
		t.Fatalf("first message: %v", err)
	}
	_ = c.Reset()
	go func() { <-ing.entered }()
	if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg)); err != nil {
		t.Errorf("budget must be released after DATA: %v", err)
	}
}

// TestShutdownCancelsPendingIngest: after the grace period the ingest context
// is cancelled, so a stuck insert rolls back instead of outliving Serve.
func TestShutdownCancelsPendingIngest(t *testing.T) {
	ing := &blockingIngester{entered: make(chan struct{}, 1), gate: make(chan struct{}), ctxErr: make(chan error, 1)}
	srv := newServer(t, Options{Ingester: ing})
	srv.grace = 50 * time.Millisecond
	addr, stop := serveWith(t, srv)

	sent := make(chan error, 1)
	go func() {
		c, err := dialRaw(addr)
		if err != nil {
			sent <- err
			return
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			sent <- err
			return
		}
		sent <- c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg))
	}()
	<-ing.entered
	stop()
	if err := <-ing.ctxErr; !errors.Is(err, context.Canceled) {
		t.Errorf("ingest context: %v, want canceled", err)
	}
	if err := <-sent; err == nil {
		t.Error("the client must not get a 250 for a cancelled ingest")
	}
}

// dialRaw is dial for goroutines, where t.Fatal is not allowed.
func dialRaw(addr string) (*smtp.Client, error) { return smtp.Dial(addr) }
