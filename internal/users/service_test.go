package users_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const pw = "a long enough passphrase"

func TestCreateValidation(t *testing.T) {
	pool := testutil.Pool(t)
	svc := users.NewService(pool)
	ctx := context.Background()
	tests := []struct {
		name string
		in   users.CreateInput
		want error
	}{
		{"valid", users.CreateInput{Email: "a@example.test", Password: pw}, nil},
		{"duplicate email ignoring case", users.CreateInput{Email: "A@Example.test", Password: pw}, users.ErrEmailTaken},
		{"short password", users.CreateInput{Email: "b@example.test", Password: "short"}, users.ErrInvalid},
		{"huge password", users.CreateInput{Email: "b@example.test", Password: strings.Repeat("x", 129)}, users.ErrInvalid},
		{"not an address", users.CreateInput{Email: "nope", Password: pw}, users.ErrInvalid},
		{"empty email", users.CreateInput{Password: pw}, users.ErrInvalid},
		{"display name in email", users.CreateInput{Email: "B <b@example.test>", Password: pw}, users.ErrInvalid},
		{"overlong name", users.CreateInput{Email: "c@example.test", Name: strings.Repeat("n", 201), Password: pw}, users.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := svc.Create(ctx, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err == nil && (!u.MustChangePassword || u.Name != "a" || u.IsAdmin) {
				t.Errorf("user: %+v", u)
			}
		})
	}
}

func TestUpdateAndLastAdmin(t *testing.T) {
	pool := testutil.Pool(t)
	svc := users.NewService(pool)
	ctx := context.Background()
	only := testutil.User(t, pool, "root@example.test", true)
	regular := testutil.User(t, pool, "dev@example.test", false)

	no, yes := false, true
	name := "Renamed"
	if _, err := svc.Update(ctx, only, users.UpdateInput{IsAdmin: &no}); !errors.Is(err, users.ErrLastAdmin) {
		t.Errorf("demote last admin: %v", err)
	}
	if _, err := svc.Update(ctx, only, users.UpdateInput{Disabled: &yes}); !errors.Is(err, users.ErrLastAdmin) {
		t.Errorf("disable last admin: %v", err)
	}
	if u, err := svc.Update(ctx, only, users.UpdateInput{Name: &name}); err != nil || u.Name != name {
		t.Errorf("rename: %+v %v", u, err)
	}
	if _, err := svc.Update(ctx, 999999, users.UpdateInput{Name: &name}); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
	if _, err := svc.Update(ctx, regular, users.UpdateInput{Name: ptr(strings.Repeat("n", 201))}); !errors.Is(err, users.ErrInvalid) {
		t.Errorf("long name: %v", err)
	}

	promoted, err := svc.Update(ctx, regular, users.UpdateInput{IsAdmin: &yes})
	if err != nil || !promoted.IsAdmin {
		t.Fatalf("promote: %+v %v", promoted, err)
	}
	if _, err := svc.Update(ctx, only, users.UpdateInput{Disabled: &yes}); err != nil {
		t.Errorf("disable with another admin around: %v", err)
	}
	u, err := svc.Get(ctx, only)
	if err != nil || u.DisabledAt == nil {
		t.Errorf("disabled_at not set: %+v %v", u, err)
	}
	re, err := svc.Update(ctx, only, users.UpdateInput{Disabled: &no})
	if err != nil || re.DisabledAt != nil {
		t.Errorf("re-enable: %+v %v", re, err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Errorf("list: %v %v", list, err)
	}
	if _, err := svc.Get(ctx, 999999); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPasswordFlows(t *testing.T) {
	pool := testutil.Pool(t)
	svc := users.NewService(pool)
	ctx := context.Background()
	id := testutil.UserWithPassword(t, pool, "u@example.test", pw, false, false)
	q := sqlcgen.New(pool)

	keep := []byte("keep-session-id-0123456789abcdef")
	drop := []byte("drop-session-id-0123456789abcdef")
	for _, sid := range [][]byte{keep, drop} {
		if err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: sid, UserID: id, ExpiresAt: farFuture()}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		cur, next string
		want      error
	}{
		{"wrong current", "not the password!!", "brand new passphrase", users.ErrWrongPassword},
		{"too short", pw, "short", users.ErrInvalid},
		{"unchanged", pw, pw, users.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.ChangePassword(ctx, id, tc.cur, tc.next, keep); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if err := svc.ChangePassword(ctx, 999999, pw, "brand new passphrase", keep); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
	if err := svc.ChangePassword(ctx, id, pw, "brand new passphrase", keep); err != nil {
		t.Fatal(err)
	}
	hash, _ := q.GetUserPasswordHash(ctx, id)
	if passwords.Verify("brand new passphrase", hash) != nil {
		t.Error("new password not stored")
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1", id).Scan(&n); err != nil || n != 1 {
		t.Errorf("only the caller's session may survive: %d %v", n, err)
	}

	if err := svc.ResetPassword(ctx, id, "short"); !errors.Is(err, users.ErrInvalid) {
		t.Errorf("reset short: %v", err)
	}
	if err := svc.ResetPassword(ctx, 999999, "another passphrase!"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("reset missing: %v", err)
	}
	if err := svc.ResetPassword(ctx, id, "another passphrase!"); err != nil {
		t.Fatal(err)
	}
	u, _ := svc.Get(ctx, id)
	if !u.MustChangePassword {
		t.Error("a reset must force a change")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1", id).Scan(&n); err != nil || n != 0 {
		t.Errorf("a reset must end every session: %d %v", n, err)
	}
}
