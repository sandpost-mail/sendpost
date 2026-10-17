package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const testKey = "0123456789abcdef0123456789abcdef"

func testEnv(vars map[string]string) (env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return env{
		getenv: func(k string) string { return vars[k] },
		stdout: &out,
		stderr: &errb,
	}, &out, &errb
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		vars       map[string]string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args", nil, nil, exitUsage, "", "Usage:"},
		{"help", []string{"help"}, nil, exitOK, "Usage:", ""},
		{"unknown", []string{"bogus"}, nil, exitUsage, "", `unknown command "bogus"`},
		{"admin help", []string{"admin"}, nil, exitOK, "Usage: sandpost admin", ""},
		{"admin unknown task", []string{"admin", "x"}, nil, exitError, "", "unknown admin task"},
		{"serve without config", []string{"serve"}, nil, exitError, "", "DATABASE_URL is required"},
		{"migrate without config", []string{"migrate"}, nil, exitError, "", "DATABASE_URL is required"},
		{"migrate bad direction", []string{"migrate", "sideways"}, nil, exitError, "", "unknown migrate direction"},
		{"migrate too many args", []string{"migrate", "up", "x"}, nil, exitError, "", "usage:"},
		{
			"migrate unreachable db", []string{"migrate"},
			map[string]string{"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db?connect_timeout=1", "SECRET_KEY": testKey},
			exitError, "", "ping database",
		},
		{
			"serve unreachable db", []string{"serve"},
			map[string]string{"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db?connect_timeout=1", "SECRET_KEY": testKey},
			exitError, "", "ping database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out, errb := testEnv(tt.vars)
			if code := run(context.Background(), e, tt.args); code != tt.wantCode {
				t.Fatalf("code %d want %d (stderr %q)", code, tt.wantCode, errb)
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout %q missing %q", out, tt.wantStdout)
			}
			if !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr %q missing %q", errb, tt.wantStderr)
			}
		})
	}
}

func TestMigrateAndServe(t *testing.T) {
	dsn := testutil.PostgresDSN(t)
	vars := map[string]string{"DATABASE_URL": dsn, "SECRET_KEY": testKey}
	ctx := context.Background()

	for _, step := range []struct{ arg, want string }{
		{"status", "pending"},
		{"up", "applied 4 migration(s)"},
		{"status", "applied"},
		{"down", "rolled back"},
		{"up", "applied 1 migration(s)"},
	} {
		e, out, errb := testEnv(vars)
		if code := run(ctx, e, []string{"migrate", step.arg}); code != exitOK {
			t.Fatalf("migrate %s: code %d stderr %q", step.arg, code, errb)
		}
		if !strings.Contains(out.String(), step.want) {
			t.Fatalf("migrate %s: stdout %q missing %q", step.arg, out, step.want)
		}
	}

	addr := freeAddr(t)
	vars["HTTP_LISTEN"] = addr
	vars["SMTP_LISTEN"] = freeAddr(t)
	e, _, errb := testEnv(vars)
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- run(sctx, e, []string{"serve"}) }()

	for _, path := range []string{"/healthz", "/readyz"} {
		if code := waitStatus(t, "http://"+addr+path); code != http.StatusOK {
			t.Errorf("%s: got %d", path, code)
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("serve exit %d stderr %q", code, errb)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServeListenError(t *testing.T) {
	dsn := testutil.PostgresDSN(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	e, _, errb := testEnv(map[string]string{
		"DATABASE_URL": dsn, "SECRET_KEY": testKey, "HTTP_LISTEN": ln.Addr().String(),
		"SMTP_LISTEN": freeAddr(t),
	})
	if code := run(context.Background(), e, []string{"serve"}); code != exitError {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "listen http") {
		t.Errorf("stderr %q", errb)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func waitStatus(t *testing.T, url string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec,noctx // test against local server
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server never answered %s", url)
	return 0
}

func TestServeHTTPServerError(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // Serve fails immediately on a closed listener
	err = serveHTTP(context.Background(), ln, http.NotFoundHandler(), discardLogger())
	if err == nil {
		t.Fatal("expected error")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
