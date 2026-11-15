// Package store owns database access: the pgx pool, embedded goose migrations
// and the sqlc generated queries.
package store

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pingTimeout = 5 * time.Second
	// minMaxConns is the floor of the default pool size. The pool is shared by
	// the HTTP API, SMTP ingest, wait queries, River's LISTEN connection and
	// its workers, so the pgx default of max(4, NumCPU) is too small.
	minMaxConns = 10
	// maxConnsParam is the DSN parameter that overrides the default size.
	maxConnsParam = "pool_max_conns"
)

// DefaultMaxConns is the pool size used when the DSN does not set
// pool_max_conns: max(10, 4*NumCPU).
func DefaultMaxConns() int32 {
	n := max(minMaxConns, 4*runtime.NumCPU())
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	return int32(n)
}

// Connect opens a pgx pool and verifies connectivity. The pool holds
// DefaultMaxConns connections unless the DSN sets pool_max_conns.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if !strings.Contains(databaseURL, maxConnsParam) {
		cfg.MaxConns = DefaultMaxConns()
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
