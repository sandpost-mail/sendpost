// Package testutil holds helpers shared by integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/sandpost-mail/sandpost/internal/store"
)

const (
	envTestDatabaseURL = "TEST_DATABASE_URL"
	postgresImage      = "postgres:17-alpine"

	// templatePrefix names the migrated template database. The schema
	// fingerprint is appended, so a new migration gets a new template and an
	// unchanged schema reuses the one left by an earlier run.
	templatePrefix = "sandpost_tmpl_"
	// templateLockKey serializes template creation between test binaries
	// running in parallel against the same server.
	templateLockKey = 0x5a4d_7e57
	// Cloning fails with object_in_use while another session is connected to
	// the template (a parallel binary finishing its creation): retry briefly.
	cloneAttempts = 50
	cloneBackoff  = 100 * time.Millisecond
	sqlObjectBusy = "55006"
	cleanupBudget = 30 * time.Second
)

var (
	sharedOnce sync.Once
	sharedDSN  string
	sharedErr  error

	templateOnce sync.Once
	templateName string
	templateErr  error
)

// PostgresDSN returns the DSN of a fresh, empty database dedicated to the
// calling test, dropped when the test ends. It uses TEST_DATABASE_URL (a server
// where the user may create databases, as with a CI service container) when
// set, otherwise it starts a single shared Testcontainers Postgres for the test
// binary. The test is skipped when neither is available (for example no Docker).
func PostgresDSN(t testing.TB) string {
	t.Helper()
	return newDatabase(t, adminDSN(t), "")
}

// MigratedDSN returns the DSN of a fresh, fully migrated database dedicated to
// the calling test, dropped when the test ends. The schema is migrated once
// into a template database and each test database is a copy of it.
func MigratedDSN(t testing.TB) string {
	t.Helper()
	admin := adminDSN(t)
	templateOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		templateName, templateErr = ensureTemplate(ctx, admin)
	})
	if templateErr != nil {
		t.Fatalf("migrated template database: %v", templateErr)
	}
	return newDatabase(t, admin, templateName)
}

func adminDSN(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if dsn := os.Getenv(envTestDatabaseURL); dsn != "" {
		return dsn
	}
	// testcontainers only offers the health check for *testing.T; benchmarks
	// fall through to startContainer, whose error also skips.
	if tt, ok := t.(*testing.T); ok {
		testcontainers.SkipIfProviderIsNotHealthy(tt)
	}
	sharedOnce.Do(func() { sharedDSN, sharedErr = startContainer() })
	if sharedErr != nil {
		t.Skipf("postgres container unavailable: %v", sharedErr)
	}
	return sharedDSN
}

func startContainer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("sandpost"),
		postgres.WithPassword("sandpost"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", fmt.Errorf("start postgres container: %w", err)
	}
	// The container is reaped by Ryuk when the test binary exits.
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", fmt.Errorf("container dsn: %w", err)
	}
	return dsn, nil
}

// newDatabase creates a uniquely named database (a copy of template when not
// empty) and registers its removal. Pools opened later by the test are closed
// first, since cleanups run in reverse order; FORCE ends any leftover session.
func newDatabase(t testing.TB, admin, template string) string {
	t.Helper()
	ctx := context.Background()
	name, err := randomName("t_")
	if err != nil {
		t.Fatal(err)
	}
	if err := createDatabase(ctx, admin, name, template); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		if err := dropDatabase(dctx, admin, name); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
	})
	dsn, err := withDatabase(admin, name)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// ensureTemplate returns the name of a migrated template database for the
// embedded schema, creating it when no earlier run left one.
func ensureTemplate(ctx context.Context, admin string) (string, error) {
	fp, err := store.MigrationsFingerprint()
	if err != nil {
		return "", err
	}
	name := templatePrefix + fp[:16]

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		return "", fmt.Errorf("template lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", templateLockKey) }()

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look up template: %w", err)
	}
	if exists {
		return name, nil
	}
	if err := buildTemplate(ctx, conn, admin, name); err != nil {
		return "", err
	}
	return name, nil
}

// buildTemplate creates the migrated database name. It builds under a
// temporary name and renames once complete, so a run that dies half way never
// leaves a template that looks usable. conn is an admin connection.
func buildTemplate(ctx context.Context, conn *pgx.Conn, admin, name string) error {
	build, err := randomName(name + "_b")
	if err != nil {
		return err
	}
	if err := createDatabase(ctx, admin, build, ""); err != nil {
		return err
	}
	if err := migrateDatabase(ctx, admin, build); err != nil {
		_ = dropDatabase(context.WithoutCancel(ctx), admin, build)
		return err
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{build}.Sanitize()+" RENAME TO "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = dropDatabase(context.WithoutCancel(ctx), admin, build)
		return fmt.Errorf("rename template: %w", err)
	}
	return nil
}

func migrateDatabase(ctx context.Context, admin, name string) error {
	dsn, err := withDatabase(admin, name)
	if err != nil {
		return err
	}
	pool, err := store.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	// The pool must be closed before the database can be renamed or cloned.
	defer pool.Close()
	if _, err := store.MigrateUp(ctx, pool); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return nil
}

func createDatabase(ctx context.Context, admin, name, template string) error {
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		stmt += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	for attempt := 1; ; attempt++ {
		_, err = conn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != sqlObjectBusy || attempt == cloneAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cloneBackoff):
		}
	}
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

func dropDatabase(ctx context.Context, admin, name string) error {
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

func randomName(prefix string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random name: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func withDatabase(admin, name string) (string, error) {
	u, err := url.Parse(admin)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
