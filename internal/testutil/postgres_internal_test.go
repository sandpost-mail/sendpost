package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/store"
)

// TestBuildTemplate runs the template build that ensureTemplate skips when an
// earlier run already left the template, under a name of its own.
func TestBuildTemplate(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	name, err := randomName("sandpost_tmpltest_")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	t.Cleanup(func() { _ = dropDatabase(context.Background(), admin, name) })
	if err := buildTemplate(ctx, conn, admin, name); err != nil {
		t.Fatal(err)
	}
	// Renaming onto an existing database fails and cleans its build copy.
	if err := buildTemplate(ctx, conn, admin, name); err == nil {
		t.Error("building over an existing template must fail")
	}
	dsn, err := withDatabase(admin, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SchemaCurrent(ctx, PoolFor(t, dsn)); err != nil {
		t.Errorf("schema after migrateDatabase: %v", err)
	}
}

func TestDatabaseHelpersReportErrors(t *testing.T) {
	const down = "postgres://u:p@127.0.0.1:1/postgres?connect_timeout=1" //nolint:gosec // G101: fake DSN of a closed port
	ctx := context.Background()
	if _, err := ensureTemplate(ctx, down); err == nil {
		t.Error("ensureTemplate on an unreachable server must fail")
	}
	if err := createDatabase(ctx, down, "x", ""); err == nil {
		t.Error("createDatabase on an unreachable server must fail")
	}
	if err := dropDatabase(ctx, down, "x"); err == nil {
		t.Error("dropDatabase on an unreachable server must fail")
	}
	if err := migrateDatabase(ctx, down, "x"); err == nil {
		t.Error("migrateDatabase on an unreachable server must fail")
	}
	if _, err := withDatabase("postgres://bad host:x/", "x"); err == nil {
		t.Error("withDatabase must reject an unparsable DSN")
	}
	admin := adminDSN(t)
	if err := createDatabase(ctx, admin, "sandpost_never", "sandpost_missing_template"); err == nil {
		t.Error("cloning a missing template must fail")
	}
}
