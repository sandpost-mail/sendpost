package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestSchemaCurrent(t *testing.T) {
	ctx := context.Background()
	pool := testutil.PoolFor(t, testutil.PostgresDSN(t))

	assertBehind := func(t *testing.T, step string) {
		t.Helper()
		err := store.SchemaCurrent(ctx, pool)
		if !errors.Is(err, store.ErrSchemaBehind) || !strings.Contains(err.Error(), "sandpost migrate up") {
			t.Fatalf("%s: got %v, want ErrSchemaBehind with a hint", step, err)
		}
	}

	assertBehind(t, "empty database")
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := store.SchemaCurrent(ctx, pool); err != nil {
		t.Fatalf("migrated: %v", err)
	}
	if err := store.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertBehind(t, "one migration rolled back")
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM river_migration WHERE version = (SELECT max(version) FROM river_migration)"); err != nil {
		t.Fatal(err)
	}
	assertBehind(t, "job queue migration missing")
}

func TestMigrationsFingerprint(t *testing.T) {
	a, err := store.MigrationsFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.MigrationsFingerprint()
	if err != nil || len(a) != 64 || a != b {
		t.Fatalf("fingerprint %q then %q (%v), want a stable sha256", a, b, err)
	}
}

func TestSchemaCurrentClosedPool(t *testing.T) {
	pool := testutil.PoolFor(t, testutil.PostgresDSN(t))
	pool.Close()
	if err := store.SchemaCurrent(context.Background(), pool); err == nil || errors.Is(err, store.ErrSchemaBehind) {
		t.Fatalf("got %v, want a connection error", err)
	}
}
