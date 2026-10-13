// Package testutil holds helpers shared by integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	envTestDatabaseURL = "TEST_DATABASE_URL"
	postgresImage      = "postgres:17-alpine"
)

var (
	sharedOnce sync.Once
	sharedDSN  string
	sharedErr  error
)

// PostgresDSN returns the DSN of a fresh, empty database dedicated to the
// calling test. It uses TEST_DATABASE_URL (a server where the user may create
// databases, as with a CI service container) when set, otherwise it starts a
// single shared Testcontainers Postgres for the test binary. The test is
// skipped when neither is available (for example no Docker).
func PostgresDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	adminDSN := os.Getenv(envTestDatabaseURL)
	if adminDSN == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
		sharedOnce.Do(func() { sharedDSN, sharedErr = startContainer() })
		if sharedErr != nil {
			t.Skipf("postgres container unavailable: %v", sharedErr)
		}
		adminDSN = sharedDSN
	}
	dsn, err := createDatabase(context.Background(), adminDSN)
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	return dsn
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

func createDatabase(ctx context.Context, adminDSN string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random name: %w", err)
	}
	name := "t_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return "", fmt.Errorf("connect admin: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", fmt.Errorf("create database: %w", err)
	}
	u, err := url.Parse(adminDSN)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
