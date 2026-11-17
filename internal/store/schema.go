package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// ErrSchemaBehind means the database lacks migrations that this binary embeds.
var ErrSchemaBehind = errors.New("database schema is behind this binary")

// SchemaCurrent returns nil when every embedded migration (goose and the job
// queue's) is applied, and an error wrapping ErrSchemaBehind when some are
// pending. It never changes the database. A database newer than the binary
// (a rollback of the binary) is accepted.
func SchemaCurrent(ctx context.Context, pool *pgxpool.Pool) error {
	current, target, err := gooseVersions(ctx, pool)
	if err != nil {
		return err
	}
	if current < target {
		return fmt.Errorf("%w: at version %d, this binary needs %d: run `sandpost migrate up`",
			ErrSchemaBehind, current, target)
	}
	n, err := RiverPending(ctx, pool)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d job queue migration(s) pending: run `sandpost migrate up`", ErrSchemaBehind, n)
	}
	return nil
}

// gooseVersions returns the applied goose version (0 on an empty database)
// and the newest embedded one, without taking the migration lock.
func gooseVersions(ctx context.Context, pool *pgxpool.Pool) (current, target int64, err error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return 0, 0, fmt.Errorf("embedded migrations: %w", err)
	}
	// Closing this *sql.DB does not close the pool.
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return 0, 0, fmt.Errorf("goose provider: %w", err)
	}
	sources := p.ListSources()
	if len(sources) == 0 {
		return 0, 0, nil
	}
	target = sources[len(sources)-1].Version

	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&exists); err != nil {
		return 0, 0, fmt.Errorf("check migration table: %w", err)
	}
	if !exists {
		return 0, target, nil
	}
	current, err = p.GetDBVersion(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("schema version: %w", err)
	}
	return current, target, nil
}

// MigrationsFingerprint identifies the embedded schema: a hash of every goose
// migration file and of the job queue's migrations. Tests use it to name a
// reusable template database.
func MigrationsFingerprint() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(migrationsFS, "migrations", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s %d\n", name, len(data))
		_, _ = h.Write(data)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash migrations: %w", err)
	}
	m, err := rivermigrate.New(riverpgxv5.New(nil), &rivermigrate.Config{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return "", fmt.Errorf("river migrator: %w", err)
	}
	for _, v := range m.AllVersions() {
		_, _ = fmt.Fprintf(h, "river %d %s\n", v.Version, v.Name)
		_, _ = io.WriteString(h, v.SQLUp)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
