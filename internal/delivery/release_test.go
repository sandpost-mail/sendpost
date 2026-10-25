package delivery_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestReleaseSendsToAllowlistedTargets(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("capture", "", "@corp.example") // capture mode still allows a manual release
	mid := e.capture("stranger@elsewhere.test")

	rows, err := e.svc.Release(e.ctx, e.scope, mid, []string{"QA@corp.example", "qa@corp.example", "dev@corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Status != delivery.StatusPending || r.Kind != "release" || !strings.HasPrefix(r.Run, "release:") {
			t.Errorf("row %+v", r)
		}
	}
	if e.relay.Connections() != 0 {
		t.Error("release must only queue, the job sends")
	}
	jobs := e.releaseJobs()
	if len(jobs) != 1 || len(jobs[0].Targets) != 2 {
		t.Fatalf("jobs %+v", jobs)
	}
	if err := e.run(jobs[0], false); err != nil {
		t.Fatal(err)
	}
	for _, d := range e.deliveries(mid) {
		if d.Status != delivery.StatusSent {
			t.Errorf("%+v", d)
		}
	}
	env := e.relay.Envelopes()
	if len(env) != 1 || len(env[0].To) != 2 {
		t.Fatalf("envelopes %+v", env)
	}
	for _, a := range e.relay.RcptAttempts() {
		if a == "stranger@elsewhere.test" || strings.HasSuffix(a, "elsewhere.test") {
			t.Errorf("the original recipient was contacted by a release: %s", a)
		}
	}
	if strings.Contains(strings.ToLower(string(env[0].Data)), "bcc:") {
		t.Error("Bcc travelled")
	}
}

func TestReleaseIsRefusedWhenNotAllowed(t *testing.T) {
	setups := map[string]func(e *env){
		"no policy":          func(e *env) { e.transport(delivery.TLSNone, true) },
		"no transport":       func(e *env) { e.policy("allow", "", "@corp.example") },
		"transport disabled": func(e *env) { e.transport(delivery.TLSNone, false); e.policy("allow", "", "@corp.example") },
		"empty allowlist":    func(e *env) { e.transport(delivery.TLSNone, true); e.policy("allow", "") },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, testutil.RelayOptions{})
			setup(e)
			mid := e.capture("x@elsewhere.test")
			_, err := e.svc.Release(e.ctx, e.scope, mid, []string{"a@corp.example"})
			mustErrIs(t, err, policy.ErrBlocked)
			assertNoJobNoRows(t, e, mid)
		})
	}
	t.Run("one target off the allowlist refuses all", func(t *testing.T) {
		e := newEnv(t, testutil.RelayOptions{})
		e.transport(delivery.TLSNone, true)
		e.policy("redirect", "pm@corp.example", "@corp.example") // redirect mode does not widen releases
		mid := e.capture("x@elsewhere.test")
		_, err := e.svc.Release(e.ctx, e.scope, mid, []string{"a@corp.example", "evil@evil.test"})
		mustErrIs(t, err, policy.ErrBlocked)
		assertNoJobNoRows(t, e, mid)
	})
	t.Run("invalid targets", func(t *testing.T) {
		e := newEnv(t, testutil.RelayOptions{})
		e.transport(delivery.TLSNone, true)
		e.policy("allow", "", "@corp.example")
		mid := e.capture("x@elsewhere.test")
		for _, targets := range [][]string{nil, {}, {""}, {"nope"}, {"a@corp.example\r\nRCPT TO:<x@evil.test>"}, {"A <a@corp.example>"}} {
			if _, err := e.svc.Release(e.ctx, e.scope, mid, targets); !errors.Is(err, policy.ErrBlocked) {
				t.Errorf("%q: %v", targets, err)
			}
		}
		assertNoJobNoRows(t, e, mid)
	})
}

func assertNoJobNoRows(t *testing.T, e *env, mid int64) {
	t.Helper()
	if n := len(e.releaseJobs()); n != 0 {
		t.Errorf("%d release job(s) queued", n)
	}
	if rows := e.deliveries(mid); len(rows) != 0 {
		t.Errorf("rows %+v", rows)
	}
	if e.relay.Connections() != 0 {
		t.Error("the relay was contacted")
	}
}

// The job evaluates again: an allowlist narrowed after the release was queued stops it.
func TestReleaseIsEvaluatedAgainWhenTheJobRuns(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	mid := e.capture("x@elsewhere.test")
	if _, err := e.svc.Release(e.ctx, e.scope, mid, []string{"a@corp.example"}); err != nil {
		t.Fatal(err)
	}
	e.policy("allow", "", "@other.example")
	if err := e.run(e.releaseJobs()[0], false); err != nil {
		t.Fatal(err)
	}
	if e.relay.Connections() != 0 {
		t.Error("a release that no longer passes the allowlist was sent")
	}
	if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusBlocked {
		t.Errorf("statuses %v", st)
	}
}

func TestReleaseIsAudited(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	mid := e.capture("x@elsewhere.test")
	if _, err := e.svc.Release(e.ctx, e.scope, mid, []string{"a@corp.example"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.pool.QueryRow(e.ctx, "SELECT count(*) FROM audit_log WHERE action = 'message.release' AND project_id = $1", e.pid).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows %d %v", n, err)
	}
}

func TestReleaseWithoutQueueFails(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	svc := delivery.NewService(e.pool, testKey, &delivery.SMTPRelay{}, nil)
	if _, err := svc.Release(e.ctx, e.scope, 1, []string{"a@corp.example"}); err == nil {
		t.Error("release without a queue must fail")
	}
}

type failingQueue struct{}

func (failingQueue) InsertTx(context.Context, pgx.Tx, delivery.DeliverArgs) error {
	return errors.New("queue is down")
}

// A job that can not be queued rolls the release back: no rows without a job.
func TestReleaseRollsBackWhenTheJobCanNotBeQueued(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	mid := e.capture("x@elsewhere.test")
	svc := delivery.NewService(e.pool, testKey, &delivery.SMTPRelay{}, failingQueue{})
	if _, err := svc.Release(e.ctx, e.scope, mid, []string{"a@corp.example"}); err == nil {
		t.Fatal("want an error")
	}
	if rows := e.deliveries(mid); len(rows) != 0 {
		t.Errorf("rows %+v", rows)
	}
}

// Capture must succeed whatever happens to the delivery hook.
func TestIngestSurvivesAFailingHook(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.policy("allow", "", "@corp.example")
	failing := ingest.NewService(e.pool).WithHook(delivery.NewEnqueuer(failingQueue{}), nil)
	mid := e.captureIn(failing, e.pid, "a@corp.example")

	var n int
	if err := e.pool.QueryRow(e.ctx, "SELECT count(*) FROM messages WHERE id = $1", mid).Scan(&n); err != nil || n != 1 {
		t.Errorf("the message must be stored: %d %v", n, err)
	}
	if got := len(e.jobArgs()); got != 0 {
		t.Errorf("%d job(s)", got)
	}
	// The savepoint was rolled back, the transaction was still usable: tags and recipients exist.
	if err := e.pool.QueryRow(e.ctx, "SELECT count(*) FROM message_recipients WHERE message_id = $1", mid).Scan(&n); err != nil || n == 0 {
		t.Errorf("recipients %d %v", n, err)
	}
}

func TestIngestHookRunsInTheIngestTransaction(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.policy("redirect", "pm@corp.example")
	mid := e.capture("a@elsewhere.test")
	jobs := e.jobArgs()
	if len(jobs) != 1 || jobs[0].MessageID != mid || jobs[0].ProjectID != e.pid {
		t.Errorf("jobs %+v", jobs)
	}
}
