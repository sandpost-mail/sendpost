package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const (
	secretKey = "0123456789abcdef0123456789abcdef"
	password  = "a long enough passphrase"
)

func TestLogin(t *testing.T) {
	pool := testutil.Pool(t)
	svc, err := auth.NewService(pool, secretKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := testutil.UserWithPassword(t, pool, "dev@example.test", password, true, true)
	off := testutil.UserWithPassword(t, pool, "off@example.test", password, false, false)
	if _, err := sqlcgen.New(pool).UpdateUser(ctx, sqlcgen.UpdateUserParams{ID: off, Disabled: boolVal(true)}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, email, password string
		wantErr               error
	}{
		{"valid", "dev@example.test", password, nil},
		{"email ignores case", "DEV@Example.Test", password, nil},
		{"wrong password", "dev@example.test", "not the password!!", auth.ErrInvalidCredentials},
		{"unknown user", "ghost@example.test", password, auth.ErrInvalidCredentials},
		{"disabled user", "off@example.test", password, auth.ErrInvalidCredentials},
		{"empty", "", "", auth.ErrInvalidCredentials},
		{"overlong email", strings.Repeat("a", 300) + "@example.test", password, auth.ErrInvalidCredentials},
		{"overlong password", "dev@example.test", strings.Repeat("p", 2000), auth.ErrInvalidCredentials},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := svc.Login(ctx, tc.email, tc.password)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if sess.User.ID != id || !sess.User.MustChangePassword || len(sess.Token) < 40 || sess.CSRFToken == "" {
				t.Errorf("session: %+v", sess)
			}
			if time.Until(sess.ExpiresAt) > auth.SessionTTL || time.Until(sess.ExpiresAt) < auth.SessionTTL-time.Minute {
				t.Errorf("expiry: %v", sess.ExpiresAt)
			}
		})
	}
}

func TestSessionLifecycle(t *testing.T) {
	pool := testutil.Pool(t)
	svc, err := auth.NewService(pool, secretKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := testutil.UserWithPassword(t, pool, "dev@example.test", password, true, false)
	sess, err := svc.Login(ctx, "dev@example.test", password)
	if err != nil {
		t.Fatal(err)
	}

	p, err := svc.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != authz.KindSession || p.UserID != id || !p.IsAdmin || p.Ceiling != authz.RoleAdmin || !p.UsesCookie() || p.ExpiresAt.IsZero() {
		t.Errorf("principal: %+v", p)
	}

	// The CSRF token belongs to this session only.
	if !svc.CheckCSRF(p, sess.CSRFToken) || svc.CSRFToken(p) != sess.CSRFToken {
		t.Error("the login CSRF token must verify")
	}
	for _, bad := range []string{"", "x", sess.CSRFToken + "x"} {
		if svc.CheckCSRF(p, bad) {
			t.Errorf("CSRF accepted %q", bad)
		}
	}
	other, _ := svc.Login(ctx, "dev@example.test", password)
	if po, _ := svc.Authenticate(ctx, other.Token); svc.CheckCSRF(po, sess.CSRFToken) {
		t.Error("a CSRF token must not work across sessions")
	}

	for _, bad := range []string{"", "nope", strings.Repeat("x", 200)} {
		if _, err := svc.Authenticate(ctx, bad); !errors.Is(err, auth.ErrNoSession) {
			t.Errorf("Authenticate(%q) = %v", bad, err)
		}
	}

	if err := svc.Logout(ctx, authz.Principal{Kind: authz.KindUserToken}); err != nil {
		t.Errorf("logout of a token principal must be a no-op: %v", err)
	}
	if err := svc.Logout(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, sess.Token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survived logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, other.Token); err != nil {
		t.Errorf("another session must survive: %v", err)
	}
}

func TestKeysAreDerivedFromTheSecret(t *testing.T) {
	pool := testutil.Pool(t)
	a, _ := auth.NewService(pool, secretKey)
	b, _ := auth.NewService(pool, "fedcba9876543210fedcba9876543210")
	testutil.UserWithPassword(t, pool, "dev@example.test", password, false, false)
	sess, err := a.Login(context.Background(), "dev@example.test", password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authenticate(context.Background(), sess.Token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("a session must not verify under another SECRET_KEY: %v", err)
	}
}

func TestDatabaseFailureIsNotAnAuthenticationFailure(t *testing.T) {
	pool := testutil.Pool(t)
	svc, _ := auth.NewService(pool, secretKey)
	pool.Close()
	if _, err := svc.Login(context.Background(), "a@example.test", password); err == nil || errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("login on a closed pool: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), "token"); err == nil || errors.Is(err, auth.ErrNoSession) {
		t.Errorf("authenticate on a closed pool: %v", err)
	}
}
