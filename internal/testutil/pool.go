package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Pool returns a pgx pool on a fresh, fully migrated database. The pool is
// closed when the test ends.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := store.Connect(ctx, PostgresDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// PoolFor opens a pool on an existing, already migrated or empty database.
func PoolFor(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := store.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Project creates a project and returns its id.
func Project(t *testing.T, pool *pgxpool.Pool, slug string) int64 {
	t.Helper()
	p, err := sqlcgen.New(pool).CreateProject(context.Background(),
		sqlcgen.CreateProjectParams{Slug: slug, Name: slug})
	if err != nil {
		t.Fatalf("create project %q: %v", slug, err)
	}
	return p.ID
}
