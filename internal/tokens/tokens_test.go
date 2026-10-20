package tokens_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/tokens"
)

const secretKey = "0123456789abcdef0123456789abcdef"

func TestCreateValidation(t *testing.T) {
	pool := testutil.Pool(t)
	svc := tokens.NewService(pool, secretKey)
	ctx := context.Background()
	uid := testutil.User(t, pool, "u@example.test", false)
	scope := authz.Trusted(testutil.Project(t, pool, "p"))

	tests := []struct {
		name    string
		project bool
		label   string
		scopes  []string
		ok      bool
	}{
		{"user read", false, "ci", []string{"read"}, true},
		{"user manage", false, "", []string{"manage"}, true},
		{"project write", true, "ci", []string{"write"}, true},
		{"project manage is refused", true, "", []string{"manage"}, false},
		{"no scope", false, "", nil, false},
		{"unknown scope", false, "", []string{"admin"}, false},
		{"long label", false, strings.Repeat("l", 101), []string{"read"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.project {
				_, err = svc.CreateForProject(ctx, scope, tc.label, tc.scopes)
			} else {
				_, err = svc.CreateForUser(ctx, uid, tc.label, tc.scopes)
			}
			if (err == nil) != tc.ok || (err != nil && !errors.Is(err, tokens.ErrBadInput)) {
				t.Errorf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	if _, err := svc.CreateForUser(ctx, 999999, "", []string{"read"}); !errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("unknown owner: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	pool := testutil.Pool(t)
	svc := tokens.NewService(pool, secretKey)
	ctx := context.Background()
	uid := testutil.User(t, pool, "u@example.test", true)
	pid := testutil.Project(t, pool, "p")

	user, err := svc.CreateForUser(ctx, uid, "u", []string{"read", "write"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateForProject(ctx, authz.Trusted(pid), "p", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(user.Secret, "spu_") || !strings.HasPrefix(project.Secret, "spp_") {
		t.Fatalf("prefixes: %q %q", user.Secret, project.Secret)
	}

	p, err := svc.Authenticate(ctx, user.Secret)
	if err != nil || p.Kind != authz.KindUserToken || p.UserID != uid || !p.IsAdmin || p.Ceiling != authz.RoleMember || p.TokenID != user.ID || p.UsesCookie() {
		t.Errorf("user principal: %+v %v", p, err)
	}
	p, err = svc.Authenticate(ctx, project.Secret)
	if err != nil || p.Kind != authz.KindProjectToken || p.ProjectID != pid || p.UserID != 0 || p.Ceiling != authz.RoleViewer {
		t.Errorf("project principal: %+v %v", p, err)
	}

	for _, bad := range []string{"", "spu_", user.Secret + "x", "x" + user.Secret, strings.ToUpper(user.Secret), "sps_" + user.Secret[4:]} {
		if _, err := svc.Authenticate(ctx, bad); !errors.Is(err, tokens.ErrInvalid) {
			t.Errorf("Authenticate(%q) = %v", bad, err)
		}
	}
	other := tokens.NewService(pool, "fedcba9876543210fedcba9876543210")
	if _, err := other.Authenticate(ctx, user.Secret); !errors.Is(err, tokens.ErrInvalid) {
		t.Errorf("a token must not verify under another SECRET_KEY: %v", err)
	}
}

func TestProjectTokensNeverExceedMember(t *testing.T) {
	pool := testutil.Pool(t)
	svc := tokens.NewService(pool, secretKey)
	ctx := context.Background()
	pid := testutil.Project(t, pool, "p")
	// A row stored with the widest scope, for example by a future bug or manual edit.
	issued, err := svc.CreateForProject(ctx, authz.Trusted(pid), "", []string{"write"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_tokens SET scopes = ARRAY['manage'] WHERE id = $1", issued.ID); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, issued.Secret)
	if err != nil || p.Ceiling != authz.RoleMember {
		t.Errorf("project token ceiling: %+v %v", p, err)
	}
}

func TestRevocationAndDisabledOwners(t *testing.T) {
	pool := testutil.Pool(t)
	svc := tokens.NewService(pool, secretKey)
	ctx := context.Background()
	uid := testutil.User(t, pool, "u@example.test", false)
	pid := testutil.Project(t, pool, "p")
	scope, other := authz.Trusted(pid), authz.Trusted(testutil.Project(t, pool, "q"))

	user, _ := svc.CreateForUser(ctx, uid, "", []string{"read"})
	project, _ := svc.CreateForProject(ctx, scope, "", []string{"read"})

	// Another owner or project can not revoke it, nothing changes.
	if err := svc.RevokeForUser(ctx, uid+1, user.ID); !errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("revoke as another user: %v", err)
	}
	if err := svc.RevokeForProject(ctx, other, project.ID); !errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("revoke from another project: %v", err)
	}
	if err := svc.RevokeForProject(ctx, scope, user.ID); !errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("a user token is not a project token: %v", err)
	}
	if _, err := svc.Authenticate(ctx, project.Secret); err != nil {
		t.Errorf("token must still work: %v", err)
	}

	lists := func() (int, int) {
		u, _ := svc.ListForUser(ctx, uid)
		p, _ := svc.ListForProject(ctx, scope)
		return len(u), len(p)
	}
	if nu, np := lists(); nu != 1 || np != 1 {
		t.Errorf("lists: %d %d", nu, np)
	}
	if l, _ := svc.ListForProject(ctx, other); len(l) != 0 {
		t.Errorf("tokens leaked across projects: %+v", l)
	}

	if err := svc.RevokeForProject(ctx, scope, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeForProject(ctx, scope, project.ID); !errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("double revoke: %v", err)
	}
	if _, err := svc.Authenticate(ctx, project.Secret); !errors.Is(err, tokens.ErrInvalid) {
		t.Errorf("revoked project token: %v", err)
	}

	if _, err := svc.Authenticate(ctx, user.Secret); err != nil {
		t.Fatal(err)
	}
	disabled := true
	_, err := sqlcgen.New(pool).UpdateUser(ctx, sqlcgen.UpdateUserParams{ID: uid, Disabled: pgBool(disabled)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, user.Secret); !errors.Is(err, tokens.ErrInvalid) {
		t.Errorf("token of a disabled user: %v", err)
	}
	if err := svc.RevokeForUser(ctx, uid, user.ID); err != nil {
		t.Errorf("revoke: %v", err)
	}
}

func TestDatabaseFailure(t *testing.T) {
	pool := testutil.Pool(t)
	svc := tokens.NewService(pool, secretKey)
	uid := testutil.User(t, pool, "u@example.test", false)
	issued, err := svc.CreateForUser(context.Background(), uid, "", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	ctx := context.Background()
	if _, err := svc.Authenticate(ctx, issued.Secret); err == nil || errors.Is(err, tokens.ErrInvalid) {
		t.Errorf("authenticate: %v", err)
	}
	if _, err := svc.ListForUser(ctx, uid); err == nil {
		t.Error("list user")
	}
	if _, err := svc.ListForProject(ctx, authz.Trusted(1)); err == nil {
		t.Error("list project")
	}
	if err := svc.RevokeForUser(ctx, uid, 1); err == nil || errors.Is(err, tokens.ErrNotFound) {
		t.Errorf("revoke: %v", err)
	}
	if _, err := svc.CreateForUser(ctx, uid, "", []string{"read"}); err == nil {
		t.Error("create")
	}
}
