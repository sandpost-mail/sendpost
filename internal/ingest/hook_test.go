package ingest_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// faultyDB wraps a real TxBeginner so the hook savepoint can fail to roll
// back or to release, without touching the database: the transaction then
// stays in whatever state the hook left it.
type faultyDB struct {
	ingest.TxBeginner
	failRollback, failRelease bool
}

func (f faultyDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := f.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return faultyTx{Tx: tx, db: f}, nil
}

type faultyTx struct {
	pgx.Tx
	db faultyDB
}

func (t faultyTx) Begin(ctx context.Context) (pgx.Tx, error) {
	sp, err := t.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return faultySavepoint{Tx: sp, db: t.db}, nil
}

type faultySavepoint struct {
	pgx.Tx
	db faultyDB
}

func (s faultySavepoint) Rollback(ctx context.Context) error {
	if s.db.failRollback {
		return errors.New("injected: rollback to savepoint failed")
	}
	return s.Tx.Rollback(ctx)
}

func (s faultySavepoint) Commit(ctx context.Context) error {
	if s.db.failRelease {
		return errors.New("injected: release savepoint failed")
	}
	return s.Tx.Commit(ctx)
}

type hookFunc func(ctx context.Context, tx pgx.Tx, projectID, messageID int64) error

func (f hookFunc) AfterInsert(ctx context.Context, tx pgx.Tx, projectID, messageID int64) error {
	return f(ctx, tx, projectID, messageID)
}

func TestHookFailureNeverFailsTheCapture(t *testing.T) {
	abortTx := func(ctx context.Context, tx pgx.Tx, _, _ int64) error {
		_, err := tx.Exec(ctx, "SELECT 1/0") // aborts the transaction
		return err
	}
	succeed := func(context.Context, pgx.Tx, int64, int64) error { return nil }
	tests := []struct {
		name                      string
		hook                      hookFunc
		failRollback, failRelease bool
	}{
		{"hook fails, savepoint rolled back", abortTx, false, false},
		{"hook fails, savepoint rollback fails", abortTx, true, false},
		{"hook succeeds, release fails", succeed, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testutil.Pool(t)
			pid := testutil.Project(t, pool, "alpha")
			var calls atomic.Int32
			hook := hookFunc(func(ctx context.Context, tx pgx.Tx, p, m int64) error {
				calls.Add(1)
				return tc.hook(ctx, tx, p, m)
			})
			db := faultyDB{TxBeginner: pool, failRollback: tc.failRollback, failRelease: tc.failRelease}
			svc := ingest.NewService(db).WithHook(hook, nil)

			id, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, Raw: []byte("Subject: x\r\n\r\nb")})
			if err != nil {
				t.Fatalf("a hook failure must not fail the capture: %v", err)
			}
			if calls.Load() != 1 {
				t.Errorf("hook ran %d times, want 1", calls.Load())
			}
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE id = $1", id).Scan(&n); err != nil || n != 1 {
				t.Errorf("message %d stored %d times: %v", id, n, err)
			}
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil || n != 1 {
				t.Errorf("messages %d: %v", n, err)
			}
		})
	}
}

func TestHookPanicRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	svc := ingest.NewService(pool).WithHook(hookFunc(func(context.Context, pgx.Tx, int64, int64) error {
		panic("hook bug")
	}), nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic must propagate")
			}
		}()
		_, _ = svc.Ingest(ctx, ingest.Input{ProjectID: pid, Raw: []byte("Subject: x\r\n\r\nb")})
	}()

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil || n != 0 {
		t.Errorf("messages %d after panic: %v", n, err)
	}
	if got := pool.Stat().AcquiredConns(); got != 0 {
		t.Errorf("%d connections still held after panic", got)
	}
}
