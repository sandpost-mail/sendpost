// Package users holds user management. M1 only needs the first-boot admin.
package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// BootstrapSource is the audit source of the bootstrap admin creation when
// the context carries none.
const BootstrapSource = "bootstrap"

// BootstrapAdmin creates the first admin from ADMIN_EMAIL and ADMIN_PASSWORD
// when, and only when, there are no users yet. The admin must change the
// password at first login. It returns true when a user was created. With an
// empty email it does nothing, and it never overwrites existing users, so
// restarts and concurrent replicas are safe. Email and password follow the
// same rules as Service.Create, and the creation is audited like any other.
func BootstrapAdmin(ctx context.Context, db store.DB, email, password string, log *slog.Logger) (bool, error) {
	email = strings.TrimSpace(email)
	if email == "" && password == "" {
		return false, nil
	}
	if email == "" || password == "" {
		return false, errors.New("admin bootstrap needs both an email and a password")
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return false, fmt.Errorf("admin bootstrap email: %w", err)
	}
	if err := ValidatePassword(password); err != nil {
		return false, fmt.Errorf("admin bootstrap password: %w", err)
	}
	hash, err := passwords.Hash(password)
	if err != nil {
		return false, fmt.Errorf("hash admin password: %w", err)
	}
	if _, ok := audit.Source(ctx); !ok {
		ctx = audit.WithSource(ctx, BootstrapSource)
	}
	created := false
	err = store.InTx(ctx, db, func(q *sqlcgen.Queries) error {
		id, err := q.CreateAdminIfNoUsers(ctx, sqlcgen.CreateAdminIfNoUsersParams{
			Email: email, Name: localPart(email), PasswordHash: hash,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // users already exist
		}
		if err != nil {
			return err
		}
		created = true
		return audit.Record(ctx, q, audit.Entry{
			Action: "user.create", Target: userTarget(id),
			Detail: map[string]any{"email": email, "is_admin": true},
		})
	})
	if store.IsUniqueViolation(err) {
		return false, nil // another replica won the race
	}
	if err != nil {
		return false, fmt.Errorf("create bootstrap admin: %w", err)
	}
	if !created {
		log.Debug("admin bootstrap skipped, users already exist")
		return false, nil
	}
	log.Info("bootstrap admin created, password change required at first login", "email", email)
	return true, nil
}

func localPart(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return local
}
