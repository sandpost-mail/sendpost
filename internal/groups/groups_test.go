package groups_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestGroupCrud(t *testing.T) {
	pool := testutil.Pool(t)
	svc := groups.NewService(pool)
	ctx := context.Background()

	g, err := svc.Create(ctx, "  Devs ")
	if err != nil || g.Name != "Devs" {
		t.Fatalf("create: %+v %v", g, err)
	}
	for name, in := range map[string]string{"empty": " ", "too long": strings.Repeat("g", 101)} {
		if _, err := svc.Create(ctx, in); !errors.Is(err, groups.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.Create(ctx, "devs"); !errors.Is(err, groups.ErrNameTaken) {
		t.Errorf("duplicate ignoring case: %v", err)
	}
	other, _ := svc.Create(ctx, "Ops")
	if _, err := svc.Rename(ctx, other.ID, "DEVS"); !errors.Is(err, groups.ErrNameTaken) {
		t.Errorf("rename to a taken name: %v", err)
	}
	if _, err := svc.Rename(ctx, 999999, "x"); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("rename missing: %v", err)
	}
	if _, err := svc.Rename(ctx, other.ID, ""); !errors.Is(err, groups.ErrInvalid) {
		t.Errorf("rename empty: %v", err)
	}
	if r, err := svc.Rename(ctx, other.ID, "Platform"); err != nil || r.Name != "Platform" {
		t.Errorf("rename: %+v %v", r, err)
	}
	if got, err := svc.Get(ctx, g.ID); err != nil || got.ID != g.ID {
		t.Errorf("get: %+v %v", got, err)
	}
	if _, err := svc.Get(ctx, 999999); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	uid := testutil.User(t, pool, "u@example.test", false)
	if err := svc.AddMember(ctx, g.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, g.ID, 999999); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("add unknown user: %v", err)
	}
	if err := svc.AddMember(ctx, 999999, uid); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("add to unknown group: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Devs" || list[0].MemberCount != 1 {
		t.Errorf("list: %+v %v", list, err)
	}
	members, err := svc.Members(ctx, g.ID)
	if err != nil || len(members) != 1 || members[0].ID != uid {
		t.Errorf("members: %+v %v", members, err)
	}
	if _, err := svc.Members(ctx, 999999); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("members of a missing group: %v", err)
	}
	if err := svc.RemoveMember(ctx, g.ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveMember(ctx, g.ID, uid); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	if err := svc.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, g.ID); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestProjectAssignments(t *testing.T) {
	pool := testutil.Pool(t)
	svc := groups.NewService(pool)
	ctx := context.Background()
	p1, p2 := authz.Trusted(testutil.Project(t, pool, "p1")), authz.Trusted(testutil.Project(t, pool, "p2"))
	g, _ := svc.Create(ctx, "g")

	for _, bad := range []string{"", "admin", "OWNER", "root"} {
		if err := svc.AssignRole(ctx, p1, g.ID, bad); !errors.Is(err, groups.ErrInvalid) {
			t.Errorf("role %q: %v", bad, err)
		}
	}
	if err := svc.AssignRole(ctx, p1, 999999, "viewer"); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	if err := svc.AssignRole(ctx, authz.Trusted(999999), g.ID, "viewer"); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	for _, role := range []string{"viewer", "owner", "member"} { // later calls replace the earlier role
		if err := svc.AssignRole(ctx, p1, g.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.ProjectAssignments(ctx, p1)
	if err != nil || len(got) != 1 || got[0].Role != "member" || got[0].GroupName != "g" {
		t.Errorf("assignments: %+v %v", got, err)
	}
	if other, _ := svc.ProjectAssignments(ctx, p2); len(other) != 0 {
		t.Errorf("assignments leaked across projects: %+v", other)
	}
	if err := svc.RemoveRole(ctx, p2, g.ID); !errors.Is(err, groups.ErrNotFound) {
		t.Errorf("remove from another project: %v", err)
	}
	if err := svc.RemoveRole(ctx, p1, g.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseFailure(t *testing.T) {
	pool := testutil.Pool(t)
	svc := groups.NewService(pool)
	pool.Close()
	ctx := context.Background()
	if _, err := svc.Create(ctx, "g"); err == nil || errors.Is(err, groups.ErrNameTaken) {
		t.Errorf("create: %v", err)
	}
	if _, err := svc.List(ctx); err == nil {
		t.Error("list")
	}
	if _, err := svc.Get(ctx, 1); err == nil || errors.Is(err, groups.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := svc.Members(ctx, 1); err == nil || errors.Is(err, groups.ErrNotFound) {
		t.Errorf("members: %v", err)
	}
	if _, err := svc.ProjectAssignments(ctx, authz.Trusted(1)); err == nil {
		t.Error("assignments")
	}
	if err := svc.Delete(ctx, 1); err == nil || errors.Is(err, groups.ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
}
