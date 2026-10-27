package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const testSecretKey = "0123456789abcdef0123456789abcdef"

// app is a full server on a fresh database. Requests made with do carry the
// bearer token of a seeded instance administrator, so tests can exercise the
// endpoints without going through argon2 login.
type app struct {
	t     *testing.T
	pool  *pgxpool.Pool
	srv   *httptest.Server
	ing   *ingest.Service
	toks  *tokens.Service
	admin int64
	token string
}

func newApp(t *testing.T, opts ...func(*api.Deps)) *app {
	t.Helper()
	dsn := testutil.PostgresDSN(t)
	pool := testutil.PoolFor(t, dsn)
	if _, err := store.MigrateUp(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus(dsn, log)
	bctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.Run(bctx) }()
	t.Cleanup(func() { cancel(); <-done })

	jobs, err := delivery.NewRiverClient(pool, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	authSvc, err := auth.NewService(pool, testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	toks := tokens.NewService(pool, testSecretKey)
	deps := api.Deps{
		DB: pool, Authz: authz.New(pool), Auth: authSvc, Tokens: toks,
		Users: users.NewService(pool), Groups: groups.NewService(pool), Audit: audit.NewReader(pool),
		Signer:   authz.NewSigner(testSecretKey),
		Projects: projects.New(pool), Credentials: credentials.New(pool, testSecretKey),
		Messages: messages.New(pool), Bus: bus, Log: log,
		Delivery: delivery.NewService(pool, testSecretKey, &delivery.SMTPRelay{}, delivery.NewRiverQueue(jobs)),
		Info:     api.ServerInfo{SMTPPort: 1025, SMTPTLSMode: "off", Version: "test"},
		UI:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ui")) }),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := httptest.NewServer(api.NewHandler(deps))
	t.Cleanup(srv.Close)
	a := &app{t: t, pool: pool, srv: srv, ing: ingest.NewService(pool), toks: toks}
	a.admin = testutil.User(t, pool, "admin@example.test", true)
	a.token = a.userToken(a.admin, "manage")
	return a
}

// userToken issues an API token for a user and returns its secret.
func (a *app) userToken(uid int64, scopes ...string) string {
	a.t.Helper()
	issued, err := a.toks.CreateForUser(context.Background(), uid, "test", scopes)
	if err != nil {
		a.t.Fatal(err)
	}
	return issued.Secret
}

// projectToken issues a project API token and returns its secret.
func (a *app) projectToken(pid int64, scopes ...string) string {
	a.t.Helper()
	issued, err := a.toks.CreateForProject(context.Background(), authz.Trusted(pid), "test", scopes)
	if err != nil {
		a.t.Fatal(err)
	}
	return issued.Secret
}

// do sends a request as the seeded administrator.
func (a *app) do(method, path string, body any) (*http.Response, []byte) {
	a.t.Helper()
	return a.doAs(a.token, method, path, body)
}

// doAs sends a request with a bearer token, or anonymously for an empty one.
func (a *app) doAs(token, method, path string, body any) (*http.Response, []byte) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (a *app) mustJSON(method, path string, body any, wantStatus int, out any) {
	a.t.Helper()
	a.mustJSONAs(a.token, method, path, body, wantStatus, out)
}

func (a *app) mustJSONAs(token, method, path string, body any, wantStatus int, out any) {
	a.t.Helper()
	resp, data := a.doAs(token, method, path, body)
	if resp.StatusCode != wantStatus {
		a.t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

func (a *app) project(name string) projects.Project {
	var p projects.Project
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": name}, http.StatusCreated, &p)
	return p
}

func (a *app) capture(pid int64, fixtureName string) int64 {
	a.t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", fixtureName)) //nolint:gosec // test fixture path
	if err != nil {
		a.t.Fatal(err)
	}
	id, err := a.ing.Ingest(context.Background(), ingest.Input{ProjectID: pid, EnvelopeTo: []string{"x@example.test"}, Raw: raw})
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func pp(pid int64, format string, args ...any) string {
	return fmt.Sprintf("%s/projects/%d", api.V1Prefix, pid) + fmt.Sprintf(format, args...)
}
