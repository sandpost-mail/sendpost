package delivery_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const testKey = "0123456789abcdef0123456789abcdef"

// env is a project with a fake relay, a service and a worker on a fresh database.
type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	relay  *testutil.FakeRelay
	pid    int64
	scope  authz.Scope
	svc    *delivery.Service
	worker *delivery.Worker
	queue  delivery.Queue
	ing    *ingest.Service
	jobs   *river.Client[pgx.Tx]
}

func newEnv(t *testing.T, relayOpts testutil.RelayOptions) *env {
	t.Helper()
	pool := testutil.Pool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	relayServer := testutil.StartRelay(t, relayOpts)
	client := &delivery.SMTPRelay{}
	if relayServer.Roots != nil {
		client = &delivery.SMTPRelay{TLSConfig: &tls.Config{RootCAs: relayServer.Roots, MinVersion: tls.VersionTLS12}}
	}
	jobs, err := delivery.NewRiverClient(pool, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	queue := delivery.NewRiverQueue(jobs)
	pid := testutil.Project(t, pool, "demo")
	return &env{
		t: t, ctx: context.Background(), pool: pool, relay: relayServer, pid: pid, scope: authz.Trusted(pid),
		svc:    delivery.NewService(pool, testKey, client, queue),
		worker: delivery.NewWorker(pool, testKey, client, log),
		queue:  queue, jobs: jobs,
		ing: ingest.NewService(pool).WithHook(delivery.NewEnqueuer(queue), log),
	}
}

// transport stores a transport for the fake relay and enables it.
func (e *env) transport(mode string, enable bool) {
	e.t.Helper()
	in := delivery.TransportInput{Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: mode}
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		e.t.Fatal(err)
	}
	if enable {
		in.Enabled = true
		if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) policy(mode string, redirect string, allow ...string) {
	e.t.Helper()
	_, err := e.svc.PutPolicy(e.ctx, e.scope, delivery.PolicyInput{
		Mode: mode, Allowlist: allow, RedirectAddress: redirect, SubjectPrefix: "[sandbox]",
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func raw(to string) string {
	return "From: App <app@example.test>\r\nTo: " + to + "\r\nCc: cc@corp.example\r\nBcc: hidden@elsewhere.test\r\nSubject: Welcome\r\n\r\nHello\r\n"
}

// capture stores a message through the real ingest service (with the delivery
// hook) and returns its id.
func (e *env) capture(to ...string) int64 {
	e.t.Helper()
	return e.captureIn(e.ing, e.pid, to...)
}

func (e *env) captureIn(ing *ingest.Service, pid int64, to ...string) int64 {
	e.t.Helper()
	id, err := ing.Ingest(e.ctx, ingest.Input{
		ProjectID: pid, EnvelopeFrom: "app@example.test", EnvelopeTo: to, Raw: []byte(raw(strings.Join(to, ", "))),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// jobArgs returns the arguments of the delivery jobs in the queue, oldest first.
func (e *env) jobArgs() []delivery.DeliverArgs {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx, "SELECT args FROM river_job WHERE kind = $1 ORDER BY id", delivery.DeliverKind)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []delivery.DeliverArgs
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			e.t.Fatal(err)
		}
		var a delivery.DeliverArgs
		if err := json.Unmarshal(b, &a); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// run executes the worker for a job, like River would.
func (e *env) run(a delivery.DeliverArgs, last bool) error {
	e.t.Helper()
	return e.worker.Process(e.ctx, a, last)
}

func (e *env) auto(mid int64) delivery.DeliverArgs {
	return delivery.DeliverArgs{ProjectID: e.pid, MessageID: mid, Run: delivery.RunAuto}
}

func (e *env) deliveries(mid int64) []delivery.Delivery {
	e.t.Helper()
	list, err := e.svc.ListDeliveries(e.ctx, e.scope, mid)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

// status maps recipient to status for the rows of a message.
func statuses(list []delivery.Delivery) map[string]string {
	out := map[string]string{}
	for _, d := range list {
		out[d.Recipient] = d.Status
	}
	return out
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// waitDone polls until a message has deliveries and none is pending.
func (e *env) waitDone(mid int64) []delivery.Delivery {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list := e.deliveries(mid)
		pending := false
		for _, d := range list {
			pending = pending || d.Status == delivery.StatusPending
		}
		if len(list) > 0 && !pending {
			return list
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("deliveries of message %d never reached a terminal state: %+v", mid, e.deliveries(mid))
	return nil
}

func mustErrIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

var messagesErrNotFound = messages.ErrNotFound

func authzTrusted(pid int64) authz.Scope { return authz.Trusted(pid) }

// releaseJobs returns only the manual release jobs.
func (e *env) releaseJobs() []delivery.DeliverArgs {
	e.t.Helper()
	var out []delivery.DeliverArgs
	for _, a := range e.jobArgs() {
		if strings.HasPrefix(a.Run, "release:") {
			out = append(out, a)
		}
	}
	return out
}
