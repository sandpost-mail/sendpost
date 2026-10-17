package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const cliPw = "correct horse battery staple"

// adminRig is a migrated database plus a way to run `sandpost admin`.
type adminRig struct {
	t    *testing.T
	vars map[string]string
	pool *pgxpool.Pool
}

func newAdminRig(t *testing.T) *adminRig {
	t.Helper()
	dsn := testutil.PostgresDSN(t)
	vars := map[string]string{"DATABASE_URL": dsn, "SECRET_KEY": testKey}
	if e, _, errb := testEnv(vars); run(context.Background(), e, []string{"migrate", "up"}) != exitOK {
		t.Fatalf("migrate: %s", errb)
	}
	return &adminRig{t: t, vars: vars, pool: testutil.PoolFor(t, dsn)}
}

type adminResult struct {
	code        int
	stdout      string
	stderr      string
	promptCalls int
}

// run executes `sandpost admin args...`. prompts feeds the terminal prompt,
// stdin feeds --password-stdin.
func (r *adminRig) run(stdin string, prompts []string, args ...string) adminResult {
	r.t.Helper()
	var out, errb bytes.Buffer
	res := adminResult{}
	e := env{
		getenv: func(k string) string { return r.vars[k] },
		stdout: &out,
		stderr: &errb,
		stdin:  strings.NewReader(stdin),
		prompt: func(string) (string, error) {
			if res.promptCalls >= len(prompts) {
				return "", errors.New("unexpected prompt")
			}
			res.promptCalls++
			return prompts[res.promptCalls-1], nil
		},
	}
	res.code = run(context.Background(), e, append([]string{"admin"}, args...))
	res.stdout, res.stderr = out.String(), errb.String()
	return res
}

func (r *adminRig) user(email string) users.User {
	r.t.Helper()
	u, err := users.NewService(r.pool).GetByEmail(context.Background(), email)
	if err != nil {
		r.t.Fatalf("user %s: %v", email, err)
	}
	return u
}

func (r *adminRig) verifyPassword(email, password string) bool {
	r.t.Helper()
	hash, err := sqlcgen.New(r.pool).GetUserPasswordHash(context.Background(), r.user(email).ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return passwords.Verify(password, hash) == nil
}

func (r *adminRig) auditEntries() []audit.Event {
	r.t.Helper()
	page, err := audit.NewReader(r.pool).ListAll(context.Background(), "", 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return page.Items
}

func TestAdminHelpAndUnknownTask(t *testing.T) {
	e, out, _ := testEnv(nil)
	if code := run(context.Background(), e, []string{"admin"}); code != exitOK || !strings.Contains(out.String(), "reset-password") {
		t.Errorf("help: code %d out %q", code, out)
	}
	e, _, errb := testEnv(nil)
	if code := run(context.Background(), e, []string{"admin", "x"}); code != exitError || !strings.Contains(errb.String(), `unknown admin task "x"`) {
		t.Errorf("unknown: code %d err %q", code, errb)
	}
}

func TestAdminArgumentErrorsNeedNoDatabase(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"create without email", []string{"create-admin"}, "usage: sandpost admin create-admin"},
		{"reset with two emails", []string{"reset-password", "a@example.test", "b@example.test"}, "usage: sandpost admin reset-password"},
		{"password flag on argv", []string{"reset-password", "--password", "x", "a@example.test"}, "flag provided but not defined"},
		{"list with args", []string{"list-users", "x"}, "usage: sandpost admin list-users"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, _, errb := testEnv(nil)
			if code := run(context.Background(), e, append([]string{"admin"}, tc.args...)); code != exitError || !strings.Contains(errb.String(), tc.want) {
				t.Errorf("code %d stderr %q, want %q", code, errb, tc.want)
			}
		})
	}
}

func TestAdminCreateAdmin(t *testing.T) {
	r := newAdminRig(t)

	res := r.run("", []string{cliPw, cliPw}, "create-admin", "root@example.test", "--name", "Root")
	if res.code != exitOK || !strings.Contains(res.stdout, "root@example.test") {
		t.Fatalf("create: %+v", res)
	}
	u := r.user("root@example.test")
	if !u.IsAdmin || !u.MustChangePassword || u.Name != "Root" || u.DisabledAt != nil {
		t.Errorf("user: %+v", u)
	}
	if !r.verifyPassword("root@example.test", cliPw) {
		t.Error("the prompted password was not stored")
	}
	entries := r.auditEntries()
	if len(entries) != 1 || entries[0].Action != "user.create" || entries[0].ActorUserID != nil {
		t.Fatalf("audit: %+v", entries)
	}
	var detail map[string]any
	if err := json.Unmarshal(entries[0].Detail, &detail); err != nil || detail["actor"] != "cli" {
		t.Errorf("audit detail %s: %v", entries[0].Detail, err)
	}
	if strings.Contains(string(entries[0].Detail), cliPw) {
		t.Error("the password leaked into the audit log")
	}

	if res := r.run("", []string{cliPw, cliPw}, "create-admin", "ROOT@example.test"); res.code != exitError || !strings.Contains(res.stderr, "already in use") {
		t.Errorf("duplicate: %+v", res)
	}
	if res := r.run("", []string{"short", "short"}, "create-admin", "new@example.test"); res.code != exitError || !strings.Contains(res.stderr, "at least") {
		t.Errorf("weak password: %+v", res)
	}
}

func TestAdminPasswordInput(t *testing.T) {
	r := newAdminRig(t)

	// From stdin, first line only, no prompt.
	res := r.run(cliPw+"\nignored\n", nil, "create-admin", "--password-stdin", "stdin@example.test")
	if res.code != exitOK || res.promptCalls != 0 || !r.verifyPassword("stdin@example.test", cliPw) {
		t.Fatalf("stdin: %+v", res)
	}
	// CRLF is trimmed, flags may follow the email.
	if res := r.run(cliPw+"2\r\n", nil, "reset-password", "stdin@example.test", "--password-stdin"); res.code != exitOK || !r.verifyPassword("stdin@example.test", cliPw+"2") {
		t.Errorf("crlf: %+v", res)
	}
	// Empty stdin.
	if res := r.run("", nil, "reset-password", "--password-stdin", "stdin@example.test"); res.code != exitError || !strings.Contains(res.stderr, "no password") {
		t.Errorf("empty stdin: %+v", res)
	}
	// A confirmation that differs.
	if res := r.run("", []string{cliPw, "different-password"}, "reset-password", "stdin@example.test"); res.code != exitError || !strings.Contains(res.stderr, "do not match") {
		t.Errorf("mismatch: %+v", res)
	}
	if !r.verifyPassword("stdin@example.test", cliPw+"2") {
		t.Error("a failed reset changed the password")
	}
}

func TestAdminResetPassword(t *testing.T) {
	r := newAdminRig(t)
	id := testutil.UserWithPassword(t, r.pool, "locked@example.test", "an-old-password!", true, false)
	sessions := sqlcgen.New(r.pool)
	if err := sessions.CreateSession(context.Background(), sqlcgen.CreateSessionParams{
		ID: []byte("session-1"), UserID: id, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	res := r.run("", []string{cliPw, cliPw}, "reset-password", "Locked@Example.test")
	if res.code != exitOK {
		t.Fatalf("reset: %+v", res)
	}
	if !r.verifyPassword("locked@example.test", cliPw) || !r.user("locked@example.test").MustChangePassword {
		t.Error("password not reset or must_change_password not set")
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE user_id = $1", id).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions left: %d %v", n, err)
	}
	entries := r.auditEntries()
	if len(entries) != 1 || entries[0].Action != "user.password_reset" || entries[0].ActorUserID != nil || !strings.Contains(string(entries[0].Detail), `"cli"`) {
		t.Errorf("audit: %+v", entries)
	}

	res = r.run("", nil, "reset-password", "nobody@example.test")
	if res.code != exitError || !strings.Contains(res.stderr, "user not found") || res.promptCalls != 0 {
		t.Errorf("unknown user must fail before prompting: %+v", res)
	}
}

func TestAdminUnlockAndPromote(t *testing.T) {
	r := newAdminRig(t)
	ctx := context.Background()
	id := testutil.User(t, r.pool, "off@example.test", false)
	yes := true
	if _, err := users.NewService(r.pool).Update(ctx, id, users.UpdateInput{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}

	if res := r.run("", nil, "unlock", "off@example.test"); res.code != exitOK {
		t.Fatalf("unlock: %+v", res)
	}
	if r.user("off@example.test").DisabledAt != nil {
		t.Error("user still disabled")
	}
	if res := r.run("", nil, "unlock", "off@example.test"); res.code != exitOK || !strings.Contains(res.stdout, "already enabled") {
		t.Errorf("second unlock: %+v", res)
	}

	if res := r.run("", nil, "promote", "off@example.test"); res.code != exitOK || !r.user("off@example.test").IsAdmin {
		t.Fatalf("promote: %+v", res)
	}
	if res := r.run("", nil, "promote", "off@example.test"); res.code != exitOK || !strings.Contains(res.stdout, "already an administrator") {
		t.Errorf("second promote: %+v", res)
	}
	if res := r.run("", nil, "promote", "nobody@example.test"); res.code != exitError || !strings.Contains(res.stderr, "user not found") {
		t.Errorf("unknown: %+v", res)
	}

	// Newest first: promote, unlock, then the setup disable (no source). The
	// no-op repeats write nothing.
	entries := r.auditEntries()
	if len(entries) != 3 {
		t.Fatalf("audit: %+v", entries)
	}
	for _, e := range entries[:2] {
		var detail map[string]any
		if err := json.Unmarshal(e.Detail, &detail); err != nil || detail["actor"] != "cli" || e.ActorUserID != nil {
			t.Errorf("entry not attributed to the cli: %+v", e)
		}
	}
}

func TestAdminListUsers(t *testing.T) {
	r := newAdminRig(t)
	testutil.User(t, r.pool, "b@example.test", false)
	testutil.User(t, r.pool, "a@example.test", true)
	res := r.run("", nil, "list-users")
	if res.code != exitOK {
		t.Fatalf("list: %+v", res)
	}
	ia, ib := strings.Index(res.stdout, "a@example.test"), strings.Index(res.stdout, "b@example.test")
	if ia < 0 || ib < ia || !strings.Contains(res.stdout, "EMAIL") {
		t.Errorf("output:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "$argon2") || strings.Contains(res.stdout, "!") {
		t.Errorf("hash leaked:\n%s", res.stdout)
	}
}
