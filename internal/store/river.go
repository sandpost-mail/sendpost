package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// The job queue (River) owns its tables and tracks its own schema versions in
// river_migration. Its migrations run right after the goose ones, through
// River's supported migrator, so `sandpost migrate up` leaves one complete
// schema and a River upgrade only needs a dependency bump. River's
// migrations are not rolled back by `migrate down`, they only add tables
// that the application does not use unless a delivery job runs.

func riverMigrator(pool *pgxpool.Pool) (*rivermigrate.Migrator[pgx.Tx], error) {
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return nil, fmt.Errorf("river migrator: %w", err)
	}
	return m, nil
}

func riverUp(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	m, err := riverMigrator(pool)
	if err != nil {
		return 0, err
	}
	res, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return 0, fmt.Errorf("river migrate up: %w", err)
	}
	return len(res.Versions), nil
}

// RiverPending returns how many River migrations are not applied yet.
func RiverPending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	m, err := riverMigrator(pool)
	if err != nil {
		return 0, err
	}
	applied, err := m.ExistingVersions(ctx)
	if err != nil {
		return 0, fmt.Errorf("river versions: %w", err)
	}
	return len(m.AllVersions()) - len(applied), nil
}

func riverStatuses(ctx context.Context, pool *pgxpool.Pool) ([]MigrationStatus, error) {
	m, err := riverMigrator(pool)
	if err != nil {
		return nil, err
	}
	applied, err := m.ExistingVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("river versions: %w", err)
	}
	done := make(map[int]bool, len(applied))
	for _, a := range applied {
		done[a.Version] = true
	}
	all := m.AllVersions()
	out := make([]MigrationStatus, 0, len(all))
	for _, v := range all {
		out = append(out, MigrationStatus{Version: int64(v.Version), Source: "river:" + v.Name, Applied: done[v.Version]})
	}
	return out, nil
}
