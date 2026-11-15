package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

func TestConnectPoolSize(t *testing.T) {
	dsn := testutil.PostgresDSN(t)
	tests := []struct {
		name string
		dsn  string
		want int32
	}{
		{"default", dsn, store.DefaultMaxConns()},
		{"dsn override", dsn + "&pool_max_conns=3", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := testutil.PoolFor(t, tc.dsn)
			if got := pool.Config().MaxConns; got != tc.want {
				t.Errorf("MaxConns = %d, want %d", got, tc.want)
			}
		})
	}
	if store.DefaultMaxConns() < 10 {
		t.Errorf("DefaultMaxConns = %d, want at least 10", store.DefaultMaxConns())
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
	if pending, err := store.RiverPending(ctx, pool); err != nil || pending != 0 {
		t.Fatalf("river migrations pending after migrate up: %d %v", pending, err)
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
	if _, err := pool.Exec(ctx, "SELECT actor_token_id FROM audit_log"); err == nil {
		t.Fatal("audit_log.actor_token_id should be gone after rolling back the latest migration")
	}
	for _, tbl := range []string{"comments", "deliveries", "delivery_policies", "transports"} {
		if _, err := pool.Exec(ctx, "SELECT 1 FROM "+tbl); err != nil {
			t.Fatalf("%s: the delivery migration must stay applied: %v", tbl, err)
		}
	}
	if _, err := pool.Exec(ctx, "SELECT 1 FROM river_job"); err != nil {
		t.Fatalf("the job queue schema is not rolled back with the goose migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP INDEX tags_project_lower_name_idx"); err != nil {
		t.Fatalf("older migrations must stay applied: %v", err)
	}
	if _, err := pool.Exec(ctx, "SELECT 1 FROM pg_proc WHERE proname = 'sp_filter_match'"); err != nil {
		t.Fatalf("older migrations must stay applied: %v", err)
	}
	if _, err := pool.Exec(ctx, "SELECT 1 FROM groups"); err != nil {
		t.Fatalf("the access control migration must stay applied: %v", err)
	}
	if _, err := pool.Exec(ctx, "SELECT 1 FROM users"); err != nil {
		t.Fatalf("older migrations must stay applied: %v", err)
	}
	if got, err := q.GetSetting(ctx, "k"); err != nil || got != "two" {
		t.Fatalf("older migrations must stay applied: %q %v", got, err)
	}
}

// TestConcurrentMigrateUp runs two migrations at once: the advisory lock
// covers goose and River, so both succeed and every migration applies once.
func TestConcurrentMigrateUp(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.PostgresDSN(t)
	a, b := testutil.PoolFor(t, dsn), testutil.PoolFor(t, dsn)
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 2)
	for _, p := range []*pgxpool.Pool{a, b} {
		go func() {
			n, err := store.MigrateUp(ctx, p)
			ch <- result{n, err}
		}()
	}
	r1, r2 := <-ch, <-ch
	if r1.err != nil || r2.err != nil {
		t.Fatalf("concurrent up: %v / %v", r1.err, r2.err)
	}
	st, err := store.MigrationStatuses(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if r1.n+r2.n != len(st) || min(r1.n, r2.n) != 0 {
		t.Errorf("applied %d + %d, want %d once", r1.n, r2.n, len(st))
	}
	if pending, err := store.RiverPending(ctx, a); err != nil || pending != 0 {
		t.Errorf("river pending %d %v", pending, err)
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

func TestViolationHelpers(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Connect(ctx, testutil.PostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := sqlcgen.New(pool)
	if _, err := q.CreateProject(ctx, sqlcgen.CreateProjectParams{Slug: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	_, err = q.CreateProject(ctx, sqlcgen.CreateProjectParams{Slug: "a", Name: "A again"})
	if !store.IsUniqueViolation(err) || store.IsForeignKeyViolation(err) {
		t.Errorf("duplicate slug: %v", err)
	}
	_, err = q.CreateCredential(ctx, sqlcgen.CreateCredentialParams{ProjectID: 999, Username: "u", SecretHmac: []byte{1}})
	if !store.IsForeignKeyViolation(err) || store.IsUniqueViolation(err) {
		t.Errorf("missing project: %v", err)
	}
	if store.IsUniqueViolation(errors.New("plain")) || store.IsForeignKeyViolation(nil) {
		t.Error("non database errors are not violations")
	}
}
