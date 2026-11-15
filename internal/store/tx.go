package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// DB is what services that write need: queries plus the ability to open a
// transaction. *pgxpool.Pool satisfies it.
type DB interface {
	sqlcgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn in a transaction and commits when it returns nil. Any error,
// and a panic in fn, rolls the transaction back so its connection returns to
// the pool. The rollback ignores cancellation of ctx, so a cancelled request
// still releases its transaction cleanly.
func InTx(ctx context.Context, db DB, fn func(q *sqlcgen.Queries) error) error {
	return InTxRaw(ctx, db, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx)) })
}

// InTxRaw is InTx for callers that need the pgx.Tx itself, for example to
// insert a River job or send a NOTIFY in the same transaction. Rollback and
// commit behave exactly as in InTx.
func InTxRaw(ctx context.Context, db DB, fn func(tx pgx.Tx) error) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		// After a successful Commit this returns ErrTxClosed, which is harmless.
		rbErr := tx.Rollback(context.WithoutCancel(ctx))
		if err != nil && rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
