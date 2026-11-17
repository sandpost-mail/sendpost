package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// served is a `sandpost serve` running in the background on ports chosen by
// the kernel (no free-port probing, so no race with other test binaries).
type served struct {
	httpAddr, smtpAddr string
	logs               *syncBuffer
	stop               func() int
}

// syncBuffer is a bytes.Buffer safe for the server's logger and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startServe runs serve with vars, both listeners on 127.0.0.1:0, and
// returns once they are bound. stop cancels serve and returns its exit code;
// it is also registered as a cleanup.
func startServe(t *testing.T, vars map[string]string) *served {
	t.Helper()
	all := map[string]string{"HTTP_LISTEN": "127.0.0.1:0", "SMTP_LISTEN": "127.0.0.1:0"}
	for k, v := range vars {
		all[k] = v
	}
	logs := &syncBuffer{}
	type addrs struct{ http, smtp string }
	bound := make(chan addrs, 1)
	e := env{
		getenv: func(k string) string { return all[k] },
		stdout: io.Discard,
		stderr: logs,
		ready:  func(h, s net.Addr) { bound <- addrs{h.String(), s.String()} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, e, []string{"serve"}) }()

	s := &served{logs: logs}
	select {
	case a := <-bound:
		s.httpAddr, s.smtpAddr = a.http, a.smtp
	case code := <-done:
		cancel()
		t.Fatalf("serve exited with %d before listening: %s", code, logs)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("serve did not start listening")
	}
	var once sync.Once
	code := -1
	s.stop = func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(30 * time.Second):
				t.Error("serve did not stop")
			}
		})
		return code
	}
	t.Cleanup(func() { s.stop() })
	return s
}

func TestServeRefusesAStaleSchema(t *testing.T) {
	e, _, errb := testEnv(map[string]string{
		"DATABASE_URL": testutil.PostgresDSN(t), "SECRET_KEY": testKey,
		"HTTP_LISTEN": "127.0.0.1:0", "SMTP_LISTEN": "127.0.0.1:0",
	})
	if code := run(context.Background(), e, []string{"serve"}); code != exitError {
		t.Fatalf("code %d, want %d", code, exitError)
	}
	if !strings.Contains(errb.String(), "run `sandpost migrate up`") {
		t.Errorf("stderr %q lacks the migrate hint", errb)
	}
}

func TestServerInfoUsesTheBoundPort(t *testing.T) {
	cfg := config.Config{SMTPListen: ":smtp"}
	info, err := serverInfo(cfg, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2525})
	if err != nil || info.SMTPPort != 2525 {
		t.Fatalf("info %+v err %v", info, err)
	}
	if _, err := serverInfo(cfg, &net.UnixAddr{Name: "/tmp/x", Net: "unix"}); err == nil {
		t.Fatal("want an error for a listener without a TCP port")
	}
}

func TestIsStream(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/_/api/v1/projects/3/events", true},
		{http.MethodGet, "/_/api/v1/projects/3/wait", true},
		{http.MethodPost, "/_/api/v1/projects/3/wait", false},
		{http.MethodGet, "/_/api/v1/projects/3/messages", false},
		{http.MethodGet, "/events", false},
	}
	for _, tt := range tests {
		r, _ := http.NewRequestWithContext(context.Background(), tt.method, tt.path, nil)
		if got := isStream(r); got != tt.want {
			t.Errorf("%s %s: %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

// startHTTP runs serveHTTPWithGrace on a fresh listener.
func startHTTP(t *testing.T, h http.Handler, grace time.Duration, log *slog.Logger) (base string, cancel func(), done <-chan error) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelCtx := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveHTTPWithGrace(ctx, ln, h, log, grace) }()
	t.Cleanup(cancelCtx)
	return "http://" + ln.Addr().String(), cancelCtx, errc
}

type result struct {
	status int
	body   string
	err    error
}

func fetch(url string) <-chan result {
	out := make(chan result, 1)
	go func() {
		resp, err := http.Get(url) //nolint:gosec,noctx // test against local server
		if err != nil {
			out <- result{err: err}
			return
		}
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		out <- result{status: resp.StatusCode, body: string(b), err: err}
	}()
	return out
}

func TestShutdownDrainsRequestsAndEndsStreams(t *testing.T) {
	inFlight, streaming := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	h := http.NewServeMux()
	h.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-release
		if r.Context().Err() != nil {
			http.Error(w, "cancelled", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "finished")
	})
	h.HandleFunc("GET /_/api/v1/projects/1/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
	})
	base, cancel, done := startHTTP(t, h, time.Minute, discardLogger())

	slow := fetch(base + "/slow")
	stream := fetch(base + "/_/api/v1/projects/1/events")
	<-inFlight
	<-streaming
	cancel()

	// The stream ends on its own, long before the grace period.
	select {
	case r := <-stream:
		if r.status != http.StatusOK {
			t.Fatalf("stream: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream was not ended by shutdown")
	}
	// The ordinary request is still running, with a live context.
	select {
	case r := <-slow:
		t.Fatalf("in-flight request ended before it finished: %+v", r)
	case err := <-done:
		t.Fatalf("server stopped with a request in flight: %v", err)
	default:
	}
	close(release)
	if r := <-slow; r.err != nil || r.status != http.StatusOK || r.body != "finished" {
		t.Fatalf("in-flight request: %+v", r)
	}
	if err := <-done; err != nil {
		t.Fatalf("serveHTTP: %v", err)
	}
}

func TestShutdownGraceExpiryIsLoggedAndCancelsRequests(t *testing.T) {
	inFlight := make(chan struct{})
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-r.Context().Done() // ignores shutdown until its context ends
	})
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	base, cancel, done := startHTTP(t, h, 50*time.Millisecond, log)
	stuck := fetch(base + "/stuck")
	<-inFlight
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveHTTP: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return after the grace period")
	}
	<-stuck
	if !strings.Contains(logs.String(), "http shutdown incomplete") {
		t.Errorf("the forced close was not logged: %s", logs)
	}
}

// fakeQueue is a job queue whose Stop returns stopErr, or blocks until its
// context ends when block is set.
type fakeQueue struct {
	block   bool
	stopErr error
}

func (f *fakeQueue) Start(context.Context) error { return nil }

func (f *fakeQueue) Stop(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.stopErr
}

func TestRunJobsStop(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		q       *fakeQueue
		wantErr error
		wantLog string
	}{
		{"clean stop", &fakeQueue{}, nil, ""},
		{"stop budget exceeded is a warning", &fakeQueue{block: true}, nil, "did not stop in time"},
		{"other stop error fails", &fakeQueue{stopErr: boom}, boom, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := runJobsWithBudget(ctx, tt.q, slog.New(slog.NewTextHandler(logs, nil)), 20*time.Millisecond)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("logs %q lack %q", logs, tt.wantLog)
			}
		})
	}
}

func TestHelpAndUsageExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"top help", []string{"--help"}, exitOK, "Usage: sandpost <command>", ""},
		{"serve -h", []string{"serve", "-h"}, exitOK, "Usage: sandpost serve", ""},
		{"serve --help", []string{"serve", "--help"}, exitOK, "Usage: sandpost serve", ""},
		{"serve extra", []string{"serve", "x"}, exitUsage, "", "unexpected argument"},
		{"migrate -h", []string{"migrate", "-h"}, exitOK, "Usage: sandpost migrate", ""},
		{"migrate bad", []string{"migrate", "sideways"}, exitUsage, "", "unknown migrate direction"},
		{"admin -h", []string{"admin", "-h"}, exitOK, "Usage: sandpost admin", ""},
		{"admin task -h", []string{"admin", "reset-password", "-h"}, exitOK, "usage: sandpost admin reset-password", ""},
		{"admin task --help", []string{"admin", "unlock", "--help"}, exitOK, "usage: sandpost admin unlock", ""},
		{"admin unknown task", []string{"admin", "bogus"}, exitUsage, "", "unknown admin task"},
		{"admin bad flag", []string{"admin", "promote", "--nope", "a@example.test"}, exitUsage, "", "flag provided but not defined"},
		{"admin missing argument", []string{"admin", "promote"}, exitUsage, "", "usage: sandpost admin promote"},
		{"wait -h", []string{"wait", "-h"}, exitOK, "Usage: sandpost wait", ""},
		{"wait bad flag", []string{"wait", "--nope"}, exitUsage, "", "flag provided but not defined"},
		{"extract -h", []string{"extract", "-h"}, exitOK, "Usage: sandpost extract", ""},
		{"extract bad print", []string{"extract", "--print", "x"}, exitUsage, "", "--print must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out, errb := testEnv(nil)
			if code := run(context.Background(), e, tt.args); code != tt.wantCode {
				t.Fatalf("code %d, want %d (stdout %q stderr %q)", code, tt.wantCode, out, errb)
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("stdout %q stderr %q, want %q / %q", out, errb, tt.wantOut, tt.wantErr)
			}
			if tt.wantCode == exitOK && errb.Len() != 0 {
				t.Errorf("help wrote to stderr: %q", errb)
			}
		})
	}
}
