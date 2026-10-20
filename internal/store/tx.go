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

// InTx runs fn in a transaction and commits when it returns nil. Any error
// rolls the transaction back.
func InTx(ctx context.Context, db DB, fn func(q *sqlcgen.Queries) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(sqlcgen.New(tx)); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
