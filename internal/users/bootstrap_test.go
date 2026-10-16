package users_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/users"
)

var log = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestBootstrapAdmin(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)

	created, err := users.BootstrapAdmin(ctx, pool, " Admin@Example.test ", "a-long-password-1", log)
	if err != nil || !created {
		t.Fatalf("first boot: %v %v", created, err)
	}
	var email, hash string
	var isAdmin, must bool
	err = pool.QueryRow(ctx, "SELECT email, password_hash, is_admin, must_change_password FROM users").
		Scan(&email, &hash, &isAdmin, &must)
	if err != nil {
		t.Fatal(err)
	}
	if email != "Admin@Example.test" || !isAdmin || !must {
		t.Errorf("row: %q admin=%v must=%v", email, isAdmin, must)
	}
	if err := passwords.Verify("a-long-password-1", hash); err != nil {
		t.Errorf("stored hash does not verify: %v", err)
	}

	// Restart with another password: existing users are never touched.
	created, err = users.BootstrapAdmin(ctx, pool, "other@example.test", "another-password-22", log)
	if err != nil || created {
		t.Fatalf("second boot: %v %v", created, err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil || n != 1 {
		t.Errorf("users: %d %v", n, err)
	}
}

func TestBootstrapAdminNoopAndValidation(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	if created, err := users.BootstrapAdmin(ctx, pool, "", "", log); err != nil || created {
		t.Errorf("unset: %v %v", created, err)
	}
	for _, c := range [][2]string{{"a@example.test", ""}, {"", "pw"}, {"not-an-email", "a-long-password-1"}} {
		if _, err := users.BootstrapAdmin(ctx, pool, c[0], c[1], log); err == nil {
			t.Errorf("%v should fail", c)
		}
	}
	pool.Close()
	if _, err := users.BootstrapAdmin(ctx, pool, "a@example.test", "a-long-password-1", log); err == nil {
		t.Error("database failure must surface")
	}
}
