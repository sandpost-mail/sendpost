// Package users holds user management. M1 only needs the first-boot admin.
package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// BootstrapAdmin creates the first admin from ADMIN_EMAIL and ADMIN_PASSWORD
// when, and only when, there are no users yet. The admin must change the
// password at first login. It returns true when a user was created. With an
// empty email it does nothing, and it never overwrites existing users, so
// restarts and concurrent replicas are safe.
func BootstrapAdmin(ctx context.Context, db sqlcgen.DBTX, email, password string, log *slog.Logger) (bool, error) {
	email = strings.TrimSpace(email)
	if email == "" && password == "" {
		return false, nil
	}
	if email == "" || password == "" {
		return false, errors.New("admin bootstrap needs both an email and a password")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return false, errors.New("admin bootstrap email is not a valid address")
	}
	hash, err := passwords.Hash(password)
	if err != nil {
		return false, fmt.Errorf("hash admin password: %w", err)
	}
	n, err := sqlcgen.New(db).CreateAdminIfNoUsers(ctx, sqlcgen.CreateAdminIfNoUsersParams{
		Email: addr.Address, Name: localPart(addr.Address), PasswordHash: hash,
	})
	if store.IsUniqueViolation(err) {
		return false, nil // another replica won the race
	}
	if err != nil {
		return false, fmt.Errorf("create bootstrap admin: %w", err)
	}
	if n == 0 {
		log.Debug("admin bootstrap skipped, users already exist")
		return false, nil
	}
	log.Info("bootstrap admin created, password change required at first login", "email", addr.Address)
	return true, nil
}

func localPart(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return local
}
