package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationStatus describes one migration and whether it has been applied.
type MigrationStatus struct {
	Version int64
	Source  string
	Applied bool
}

// migrationLockID is goose's default advisory lock key. Sandpost takes it
// itself, around both the goose and the River migrations, so it still
// excludes older binaries that let goose take it.
const migrationLockID = lock.DefaultLockID

// provider builds a goose provider over a *sql.DB wrapping the pool. The
// returned close func closes that wrapper (stopping its background
// goroutine); the pgx pool itself stays open. No goose session locker is
// configured: callers that change the schema hold migrationLockID.
func provider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, nil, fmt.Errorf("embedded migrations: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("goose provider: %w", err)
	}
	return p, func() { _ = db.Close() }, nil
}

// withMigrationLock runs fn while holding the Postgres advisory lock that
// serialises migrations, on a dedicated pool connection.
func withMigrationLock(ctx context.Context, pool *pgxpool.Pool, fn func() error) (err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID); uerr != nil {
			// The session keeps the lock: drop the connection instead of
			// returning it to the pool.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
			err = errors.Join(err, fmt.Errorf("migration unlock: %w", uerr))
		}
	}()
	return fn()
}

// MigrateUp applies all pending migrations under a Postgres advisory lock and
// returns the number applied. The job queue's own migrations run last, under
// the same lock, so concurrent `migrate up` runs are fully serialised.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	p, closeP, err := provider(pool)
	if err != nil {
		return 0, err
	}
	defer closeP()
	applied := 0
	err = withMigrationLock(ctx, pool, func() error {
		res, err := p.Up(ctx)
		applied += len(res)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		n, err := riverUp(ctx, pool)
		applied += n
		return err
	})
	return applied, err
}

// MigrateDown rolls back the most recent migration, under the migration lock.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	p, closeP, err := provider(pool)
	if err != nil {
		return err
	}
	defer closeP()
	return withMigrationLock(ctx, pool, func() error {
		if _, err := p.Down(ctx); err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		return nil
	})
}

// MigrationStatuses lists every known migration with its applied state.
func MigrationStatuses(ctx context.Context, pool *pgxpool.Pool) ([]MigrationStatus, error) {
	p, closeP, err := provider(pool)
	if err != nil {
		return nil, err
	}
	defer closeP()
	st, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(st))
	for _, s := range st {
		out = append(out, MigrationStatus{
			Version: s.Source.Version,
			Source:  s.Source.Path,
			Applied: s.State == goose.StateApplied,
		})
	}
	rs, err := riverStatuses(ctx, pool)
	if err != nil {
		return nil, err
	}
	return append(out, rs...), nil
}
