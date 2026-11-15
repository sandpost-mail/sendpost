package testutil

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/store"
)

func dbName(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

func databaseExists(t *testing.T, name string) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDatabasesAreDroppedAfterTheTest(t *testing.T) {
	tests := []struct {
		name     string
		dsn      func(testing.TB) string
		migrated bool
	}{
		{"empty", PostgresDSN, false},
		{"migrated", MigratedDSN, true},
	}
	for _, tt := range tests {
		var name string
		t.Run(tt.name, func(t *testing.T) {
			pool := PoolFor(t, tt.dsn(t))
			name = dbName(t, pool.Config().ConnString())
			if !databaseExists(t, name) {
				t.Fatalf("database %s missing during the test", name)
			}
			err := store.SchemaCurrent(context.Background(), pool)
			if tt.migrated && err != nil {
				t.Fatalf("template copy is not migrated: %v", err)
			}
			if !tt.migrated && !errors.Is(err, store.ErrSchemaBehind) {
				t.Fatalf("empty database: %v", err)
			}
		})
		if name == "" {
			t.Fatal("subtest did not run")
		}
		if databaseExists(t, name) {
			t.Errorf("%s: database %s left behind", tt.name, name)
		}
	}
}

func TestMigratedDatabasesAreIndependent(t *testing.T) {
	ctx := context.Background()
	a, b := Pool(t), Pool(t)
	Project(t, a, "only-in-a")
	var n int
	if err := b.QueryRow(ctx, "SELECT count(*) FROM projects").Scan(&n); err != nil || n != 0 {
		t.Fatalf("second database sees %d project(s) (%v)", n, err)
	}
}
