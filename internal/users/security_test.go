package users_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/secrets"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const secretKey = "0123456789abcdef0123456789abcdef"

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"a@example.test", "a@example.test", true},
		{"  Mixed@Example.test\t", "Mixed@Example.test", true},
		{"", "", false},
		{"nope", "", false},
		{"A <a@example.test>", "", false},
		{strings.Repeat("a", 250) + "@example.test", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := users.NormalizeEmail(tc.in)
			if (err == nil) != tc.ok || got != tc.want {
				t.Fatalf("NormalizeEmail(%q) = %q, %v", tc.in, got, err)
			}
			if err != nil && !errors.Is(err, users.ErrInvalid) {
				t.Errorf("error must wrap ErrInvalid: %v", err)
			}
		})
	}
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestResetPasswordRevokesTokens(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := users.NewService(pool)
	toks := tokens.NewService(pool, secrets.MustDeriveKeys(secretKey).APIToken)
	id := testutil.UserWithPassword(t, pool, "u@example.test", pw, false, false)
	issued := make([]tokens.Issued, 0, 2)
	for _, label := range []string{"a", "b"} {
		tok, err := toks.CreateForUser(ctx, id, label, []string{"read"})
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, tok)
	}

	if err := svc.ResetPassword(ctx, id, "another passphrase!"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range issued {
		if _, err := toks.Authenticate(ctx, tok.Secret); !errors.Is(err, tokens.ErrInvalid) {
			t.Errorf("token %q survived a reset: %v", tok.Label, err)
		}
	}
	var detail string
	err := pool.QueryRow(ctx, "SELECT detail->>'tokens_revoked' FROM audit_log WHERE action = 'user.password_reset'").Scan(&detail)
	if err != nil || detail != "2" {
		t.Errorf("audit tokens_revoked = %q, %v", detail, err)
	}
}

func TestChangePasswordKeepsTokensAndIsAudited(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := users.NewService(pool)
	toks := tokens.NewService(pool, secrets.MustDeriveKeys(secretKey).APIToken)
	id := testutil.UserWithPassword(t, pool, "u@example.test", pw, false, false)
	tok, err := toks.CreateForUser(ctx, id, "ci", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ChangePassword(ctx, id, "not the password!!", "brand new passphrase", nil); !errors.Is(err, users.ErrWrongPassword) {
		t.Fatalf("wrong current: %v", err)
	}
	if n := auditCount(t, pool, "user.password_change"); n != 0 {
		t.Errorf("a refused change must not be audited: %d", n)
	}
	if err := svc.ChangePassword(ctx, id, pw, "brand new passphrase", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := toks.Authenticate(ctx, tok.Secret); err != nil {
		t.Errorf("a self-service change must keep API tokens: %v", err)
	}
	if n := auditCount(t, pool, "user.password_change"); n != 1 {
		t.Errorf("password_change audit rows: %d", n)
	}
	// The old password no longer verifies inside the transaction.
	if err := svc.ChangePassword(ctx, id, pw, "yet another passphrase", nil); !errors.Is(err, users.ErrWrongPassword) {
		t.Errorf("old password accepted: %v", err)
	}
}

func TestUpdateWithoutChangeIsNotAudited(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := users.NewService(pool)
	id := testutil.User(t, pool, "u@example.test", false)
	cur, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	no := false
	for name, in := range map[string]users.UpdateInput{
		"empty":       {},
		"same values": {Name: ptr(" " + cur.Name + " "), IsAdmin: &no, Disabled: &no},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.Update(ctx, id, in)
			if err != nil || got != cur {
				t.Fatalf("update: %+v %v", got, err)
			}
		})
	}
	if n := auditCount(t, pool, "user.update"); n != 0 {
		t.Errorf("no-op updates were audited: %d", n)
	}
	if _, err := svc.Update(ctx, 999999, users.UpdateInput{}); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
	if _, err := svc.Update(ctx, id, users.UpdateInput{Name: ptr("New")}); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := pool.QueryRow(ctx, "SELECT detail::text FROM audit_log WHERE action = 'user.update'").Scan(&detail); err != nil || detail != `{"name": "New"}` {
		t.Errorf("audit detail = %s, %v", detail, err)
	}
}

func TestConcurrentLastAdminDemotion(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := users.NewService(pool)
	ids := []int64{testutil.User(t, pool, "a@example.test", true), testutil.User(t, pool, "b@example.test", true)}

	no := false
	errs := make([]error, len(ids))
	var start, wg sync.WaitGroup
	start.Add(1)
	for i, id := range ids {
		wg.Go(func() {
			start.Wait()
			_, errs[i] = svc.Update(ctx, id, users.UpdateInput{IsAdmin: &no})
		})
	}
	start.Done()
	wg.Wait()

	ok, last := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, users.ErrLastAdmin):
			last++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || last != 1 {
		t.Errorf("want exactly one demotion to pass: ok=%d last_admin=%d", ok, last)
	}
	var admins int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE is_admin AND disabled_at IS NULL").Scan(&admins); err != nil || admins != 1 {
		t.Errorf("active admins = %d, %v", admins, err)
	}
}

func TestDisableRevokesAccess(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := users.NewService(pool)
	authSvc, err := auth.NewService(pool, secrets.MustDeriveKeys(secretKey).Session, secrets.MustDeriveKeys(secretKey).CSRF)
	if err != nil {
		t.Fatal(err)
	}
	toks := tokens.NewService(pool, secrets.MustDeriveKeys(secretKey).APIToken)
	id := testutil.UserWithPassword(t, pool, "u@example.test", pw, false, false)
	sess, err := authSvc.Login(ctx, "u@example.test", pw)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := toks.CreateForUser(ctx, id, "ci", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}

	yes, no := true, false
	if _, err := svc.Update(ctx, id, users.UpdateInput{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.Authenticate(ctx, sess.Token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survived disabling: %v", err)
	}
	if _, err := toks.Authenticate(ctx, tok.Secret); !errors.Is(err, tokens.ErrInvalid) {
		t.Errorf("token survived disabling: %v", err)
	}
	if _, err := authSvc.Login(ctx, "u@example.test", pw); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("disabled user logged in: %v", err)
	}

	// Re-enabling restores the token (it was never revoked) but not the session.
	if _, err := svc.Update(ctx, id, users.UpdateInput{Disabled: &no}); err != nil {
		t.Fatal(err)
	}
	if _, err := toks.Authenticate(ctx, tok.Secret); err != nil {
		t.Errorf("token after re-enable: %v", err)
	}
	if _, err := authSvc.Authenticate(ctx, sess.Token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session came back after re-enable: %v", err)
	}
}
