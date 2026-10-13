package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestConnectErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := store.Connect(ctx, "://bad"); err == nil {
		t.Error("expected parse error")
	}
	if _, err := store.Connect(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=1"); err == nil {
		t.Error("expected ping error")
	}
}

func TestMigrateAndQueries(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Connect(ctx, testutil.PostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	st, err := store.MigrationStatuses(ctx, pool)
	if err != nil || len(st) == 0 || st[0].Applied {
		t.Fatalf("pre-migration status: %v %v", st, err)
	}

	n, err := store.MigrateUp(ctx, pool)
	if err != nil || n == 0 {
		t.Fatalf("up: n=%d err=%v", n, err)
	}
	if n, err = store.MigrateUp(ctx, pool); err != nil || n != 0 {
		t.Fatalf("second up should be a no-op: n=%d err=%v", n, err)
	}
	if st, err = store.MigrationStatuses(ctx, pool); err != nil || !st[0].Applied {
		t.Fatalf("post-migration status: %v %v", st, err)
	}

	q := sqlcgen.New(pool)
	if _, err := q.GetSetting(ctx, "k"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("want ErrNoRows, got %v", err)
	}
	for _, v := range []string{"one", "two"} {
		if err := q.SetSetting(ctx, sqlcgen.SetSettingParams{Key: "k", Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := q.GetSetting(ctx, "k"); err != nil || got != "two" {
		t.Fatalf("got %q %v", got, err)
	}

	if err := store.MigrateDown(ctx, pool); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := q.GetSetting(ctx, "k"); err == nil {
		t.Fatal("table should be gone after down")
	}
}

func TestMigrateErrorsOnClosedPool(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Connect(ctx, testutil.PostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	if _, err := store.MigrateUp(ctx, pool); err == nil {
		t.Error("up on closed pool should fail")
	}
	if err := store.MigrateDown(ctx, pool); err == nil {
		t.Error("down on closed pool should fail")
	}
	if _, err := store.MigrationStatuses(ctx, pool); err == nil {
		t.Error("status on closed pool should fail")
	}
}
