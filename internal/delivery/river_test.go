package delivery_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// immediateRetry makes River retry at once so a test does not wait for backoff.
type immediateRetry struct{}

func (immediateRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now() }

// startWorkers runs a real River client with the delivery worker.
func startWorkers(t *testing.T, e *env) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := delivery.NewRiverClient(e.pool, e.worker, log, func(cfg *river.Config) {
		cfg.RetryPolicy = immediateRetry{}
		cfg.FetchPollInterval = 150 * time.Millisecond
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	})
	// Ingest through the same queue the workers read.
	e.ing = ingest.NewService(e.pool).WithHook(delivery.NewEnqueuer(delivery.NewRiverQueue(c)), log)
}

func TestRiverRunsTheDeliveryJob(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("redirect", "pm@corp.example", "real@corp.example")
	startWorkers(t, e)

	mid := e.capture("real@corp.example", "stranger@elsewhere.test")
	rows := e.waitDone(mid)
	for _, d := range rows {
		if d.Status != delivery.StatusSent {
			t.Errorf("%+v", d)
		}
	}
	if n := len(e.relay.Envelopes()); n != 2 {
		t.Errorf("want 2 messages, got %d", n)
	}
	for _, a := range e.relay.RcptAttempts() {
		if a != "real@corp.example" && a != "pm@corp.example" {
			t.Errorf("unexpected recipient %s", a)
		}
	}
}

func TestRiverRetriesAreBounded(t *testing.T) {
	var attempts int
	e := newEnv(t, testutil.RelayOptions{RejectRcpt: func(string) *smtp.SMTPError {
		attempts++
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "try again later"}
	}})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	startWorkers(t, e)

	mid := e.capture("a@corp.example")
	rows := e.waitDone(mid)
	if len(rows) != 1 || rows[0].Status != delivery.StatusFailed || !strings.Contains(rows[0].Error, "gave up") {
		t.Fatalf("rows %+v", rows)
	}
	if attempts != delivery.MaxAttempts {
		t.Errorf("the relay was tried %d times, want exactly %d", attempts, delivery.MaxAttempts)
	}
	if rows[0].Attempts != delivery.MaxAttempts {
		t.Errorf("recorded attempts %d", rows[0].Attempts)
	}
	// And it really stopped: the job is finalized as completed (not retryable) with the budget spent.
	attempt := waitJobCompleted(t, e)
	if attempt != delivery.MaxAttempts || attempts != delivery.MaxAttempts {
		t.Errorf("job attempt %d, relay tries %d, want %d", attempt, attempts, delivery.MaxAttempts)
	}
}

func TestRiverMisconfigurationEndsWithoutRetries(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.policy("allow", "", "@corp.example") // transport missing
	startWorkers(t, e)
	mid := e.capture("a@corp.example")
	rows := e.waitDone(mid)
	if len(rows) == 0 || rows[0].Status != delivery.StatusBlocked {
		t.Fatalf("rows %+v", rows)
	}
	if e.relay.Connections() != 0 {
		t.Error("the relay was contacted")
	}
	if attempt := waitJobCompleted(t, e); attempt != 1 {
		t.Errorf("a blocked job must not be retried: attempt %d", attempt)
	}
}

// waitJobCompleted polls until the delivery job is finalized as completed and
// returns its attempt count. River finalizes after Work returns, so reading
// earlier would see a job that is still running.
func waitJobCompleted(t *testing.T, e *env) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		var attempt int
		err := e.pool.QueryRow(e.ctx, "SELECT state::text, attempt FROM river_job WHERE kind = $1", delivery.DeliverKind).Scan(&state, &attempt)
		if err == nil && state == "completed" {
			return attempt
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the delivery job was never finalized as completed")
	return 0
}
