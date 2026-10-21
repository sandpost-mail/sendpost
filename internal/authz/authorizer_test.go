package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestEffectiveRole(t *testing.T) {
	pool := testutil.Pool(t)
	a := authz.New(pool)
	ctx := context.Background()

	p1 := testutil.Project(t, pool, "p1")
	p2 := testutil.Project(t, pool, "p2")
	viewers := testutil.Group(t, pool, "viewers")
	members := testutil.Group(t, pool, "members")
	testutil.Assign(t, pool, p1, viewers, "viewer")
	testutil.Assign(t, pool, p1, members, "member")
	testutil.Assign(t, pool, p2, viewers, "owner")

	both := testutil.User(t, pool, "both@example.test", false)
	testutil.Member(t, pool, viewers, both)
	testutil.Member(t, pool, members, both)
	onlyViewer := testutil.User(t, pool, "v@example.test", false)
	testutil.Member(t, pool, viewers, onlyViewer)
	nobody := testutil.User(t, pool, "n@example.test", false)
	admin := testutil.User(t, pool, "a@example.test", true)

	session := func(uid int64, isAdmin bool) authz.Principal {
		return authz.Principal{Kind: authz.KindSession, UserID: uid, IsAdmin: isAdmin, Ceiling: authz.RoleAdmin}
	}
	tests := []struct {
		name string
		p    authz.Principal
		pid  int64
		want authz.Role
		err  error
	}{
		{"highest role among groups wins", session(both, false), p1, authz.RoleMember, nil},
		{"role differs per project", session(both, false), p2, authz.RoleOwner, nil},
		{"viewer only", session(onlyViewer, false), p1, authz.RoleViewer, nil},
		{"no group assigned is not found", session(nobody, false), p1, authz.RoleNone, authz.ErrNotFound},
		{"admin acts on every project", session(admin, true), p2, authz.RoleAdmin, nil},
		{"admin on missing project is not found", session(admin, true), 9999, authz.RoleNone, authz.ErrNotFound},
		{"missing project is not found", session(both, false), 9999, authz.RoleNone, authz.ErrNotFound},
		{"token ceiling caps the role", authz.Principal{Kind: authz.KindUserToken, UserID: both, Ceiling: authz.RoleViewer}, p1, authz.RoleViewer, nil},
		{"token ceiling never raises the role", authz.Principal{Kind: authz.KindUserToken, UserID: onlyViewer, Ceiling: authz.RoleAdmin}, p1, authz.RoleViewer, nil},
		{"project token on its project", authz.Principal{Kind: authz.KindProjectToken, ProjectID: p1, Ceiling: authz.RoleMember}, p1, authz.RoleMember, nil},
		{"project token on another project", authz.Principal{Kind: authz.KindProjectToken, ProjectID: p1, Ceiling: authz.RoleMember}, p2, authz.RoleNone, authz.ErrNotFound},
		{"zero principal", authz.Principal{}, p1, authz.RoleNone, authz.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.RoleOn(ctx, tc.p, tc.pid)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Errorf("RoleOn = %v, %v; want %v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestProjectScope(t *testing.T) {
	pool := testutil.Pool(t)
	a := authz.New(pool)
	ctx := context.Background()
	pid := testutil.Project(t, pool, "p")
	g := testutil.Group(t, pool, "g")
	testutil.Assign(t, pool, pid, g, "member")
	uid := testutil.User(t, pool, "m@example.test", false)
	testutil.Member(t, pool, g, uid)
	p := authz.Principal{Kind: authz.KindSession, UserID: uid, Ceiling: authz.RoleAdmin}

	scope, err := a.Project(ctx, p, pid, authz.CapWriteMessages)
	if err != nil || scope.ProjectID() != pid || scope.Role() != authz.RoleMember {
		t.Fatalf("member write: %+v %v", scope, err)
	}
	if _, err := a.Project(ctx, p, pid, authz.CapManageProject); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("member manage: %v, want forbidden", err)
	}
	if _, err := a.Project(ctx, p, pid+1, authz.CapReadMessages); !errors.Is(err, authz.ErrNotFound) {
		t.Errorf("foreign project: %v, want not found", err)
	}
}

func TestProjectBySlug(t *testing.T) {
	pool := testutil.Pool(t)
	a := authz.New(pool)
	ctx := context.Background()
	p1 := testutil.Project(t, pool, "alpha")
	testutil.Project(t, pool, "beta")
	g := testutil.Group(t, pool, "g")
	testutil.Assign(t, pool, p1, g, "viewer")
	uid := testutil.User(t, pool, "v@example.test", false)
	testutil.Member(t, pool, g, uid)
	user := authz.Principal{Kind: authz.KindUserToken, UserID: uid, Ceiling: authz.RoleAdmin}
	proj := authz.Principal{Kind: authz.KindProjectToken, ProjectID: p1, Ceiling: authz.RoleMember}

	tests := []struct {
		name string
		p    authz.Principal
		slug string
		cap  authz.Capability
		want int64
		err  error
	}{
		{"user with a role", user, "alpha", authz.CapReadMessages, p1, nil},
		{"project token on its own slug", proj, "alpha", authz.CapWriteMessages, p1, nil},
		{"role too low", user, "alpha", authz.CapWriteMessages, 0, authz.ErrForbidden},
		{"foreign slug looks like a missing one", user, "beta", authz.CapReadMessages, 0, authz.ErrNotFound},
		{"project token on another slug", proj, "beta", authz.CapReadMessages, 0, authz.ErrNotFound},
		{"unknown slug", user, "nope", authz.CapReadMessages, 0, authz.ErrNotFound},
		{"empty slug", user, "", authz.CapReadMessages, 0, authz.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := a.ProjectBySlug(ctx, tc.p, tc.slug, tc.cap)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err == nil && scope.ProjectID() != tc.want {
				t.Errorf("project = %d, want %d", scope.ProjectID(), tc.want)
			}
		})
	}
}
