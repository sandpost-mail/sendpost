package store

import (
	"context"
	"embed"
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

func provider(pool *pgxpool.Pool) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("embedded migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("migration locker: %w", err)
	}
	// The *sql.DB wrapper shares the pool; it is not closed here.
	p, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(pool), sub,
		goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	return p, nil
}

// MigrateUp applies all pending migrations under a Postgres advisory lock and
// returns the number applied. The job queue's own migrations run last.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	p, err := provider(pool)
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate up: %w", err)
	}
	n, err := riverUp(ctx, pool)
	if err != nil {
		return 0, err
	}
	return len(res) + n, nil
}

// MigrateDown rolls back the most recent migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	p, err := provider(pool)
	if err != nil {
		return err
	}
	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

// MigrationStatuses lists every known migration with its applied state.
func MigrationStatuses(ctx context.Context, pool *pgxpool.Pool) ([]MigrationStatus, error) {
	p, err := provider(pool)
	if err != nil {
		return nil, err
	}
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
