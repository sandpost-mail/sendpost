package authz_test

import (
	"errors"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

// TestMatrix is the authorization matrix of docs/ARCHITECTURE.md section 2.5.
func TestMatrix(t *testing.T) {
	roles := []authz.Role{authz.RoleNone, authz.RoleViewer, authz.RoleMember, authz.RoleOwner, authz.RoleAdmin}
	tests := []struct {
		cap  authz.Capability
		want map[authz.Role]bool
	}{
		{authz.CapReadMessages, map[authz.Role]bool{authz.RoleViewer: true, authz.RoleMember: true, authz.RoleOwner: true, authz.RoleAdmin: true}},
		{authz.CapWriteMessages, map[authz.Role]bool{authz.RoleMember: true, authz.RoleOwner: true, authz.RoleAdmin: true}},
		{authz.CapManageProject, map[authz.Role]bool{authz.RoleOwner: true, authz.RoleAdmin: true}},
		{authz.CapManageProjectGroups, map[authz.Role]bool{authz.RoleOwner: true, authz.RoleAdmin: true}},
		{authz.CapManageUsers, map[authz.Role]bool{authz.RoleAdmin: true}},
		{authz.CapManageGroups, map[authz.Role]bool{authz.RoleAdmin: true}},
		{authz.CapCreateProject, map[authz.Role]bool{authz.RoleAdmin: true}},
		{authz.CapReadAudit, map[authz.Role]bool{authz.RoleAdmin: true}},
	}
	if len(tests) != len(authz.Capabilities()) {
		t.Fatalf("matrix test covers %d capabilities, package defines %d", len(tests), len(authz.Capabilities()))
	}
	for _, tc := range tests {
		for _, r := range roles {
			t.Run(string(tc.cap)+"/"+r.String(), func(t *testing.T) {
				if got := r.Allows(tc.cap); got != tc.want[r] {
					t.Errorf("%s.Allows(%s) = %v, want %v", r, tc.cap, got, tc.want[r])
				}
			})
		}
	}
}

func TestUnknownCapabilityFailsClosed(t *testing.T) {
	if authz.RoleAdmin.Allows("nope") {
		t.Error("an unknown capability must be denied, even for admin")
	}
	if _, err := authz.MinRole("nope"); err == nil {
		t.Error("MinRole of an unknown capability must fail")
	}
}

func TestParseRole(t *testing.T) {
	for in, want := range map[string]authz.Role{"viewer": authz.RoleViewer, "member": authz.RoleMember, "owner": authz.RoleOwner} {
		if got, ok := authz.ParseRole(in); !ok || got != want || got.String() != in {
			t.Errorf("ParseRole(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "admin", "Owner", "root"} {
		if _, ok := authz.ParseRole(bad); ok {
			t.Errorf("ParseRole(%q) must fail", bad)
		}
	}
}

func TestCeilingOf(t *testing.T) {
	tests := []struct {
		scopes []string
		want   authz.Role
		ok     bool
	}{
		{[]string{"read"}, authz.RoleViewer, true},
		{[]string{"read", "write"}, authz.RoleMember, true},
		{[]string{"manage"}, authz.RoleAdmin, true},
		{nil, authz.RoleNone, false},
		{[]string{"read", "root"}, authz.RoleNone, false},
	}
	for _, tc := range tests {
		got, ok := authz.CeilingOf(tc.scopes)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CeilingOf(%v) = %v %v, want %v %v", tc.scopes, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGlobal(t *testing.T) {
	admin := authz.Principal{Kind: authz.KindSession, UserID: 1, IsAdmin: true, Ceiling: authz.RoleAdmin}
	tests := []struct {
		name string
		p    authz.Principal
		want error
	}{
		{"admin session", admin, nil},
		{"non admin session", authz.Principal{Kind: authz.KindSession, UserID: 2, Ceiling: authz.RoleAdmin}, authz.ErrForbidden},
		{"admin with read token", authz.Principal{Kind: authz.KindUserToken, UserID: 1, IsAdmin: true, Ceiling: authz.RoleViewer}, authz.ErrForbidden},
		{"admin with manage token", authz.Principal{Kind: authz.KindUserToken, UserID: 1, IsAdmin: true, Ceiling: authz.RoleAdmin}, nil},
		{"project token", authz.Principal{Kind: authz.KindProjectToken, ProjectID: 1, IsAdmin: true, Ceiling: authz.RoleAdmin}, authz.ErrForbidden},
		{"zero principal", authz.Principal{}, authz.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authz.Global(tc.p, authz.CapManageUsers); !errors.Is(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestProjectRoleName is the role vocabulary of the API: administrators act as
// owners, and no access has no name.
func TestProjectRoleName(t *testing.T) {
	tests := []struct {
		role authz.Role
		want string
	}{
		{authz.RoleNone, ""},
		{authz.RoleViewer, "viewer"},
		{authz.RoleMember, "member"},
		{authz.RoleOwner, "owner"},
		{authz.RoleAdmin, "owner"},
	}
	for _, tc := range tests {
		t.Run(tc.role.String(), func(t *testing.T) {
			if got := tc.role.ProjectRoleName(); got != tc.want {
				t.Errorf("ProjectRoleName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUserRole resolves group rank, instance admin flag and token ceiling.
func TestUserRole(t *testing.T) {
	tests := []struct {
		name    string
		admin   bool
		ceiling authz.Role
		rank    int32
		want    authz.Role
	}{
		{"no group role", false, authz.RoleAdmin, 0, authz.RoleNone},
		{"viewer", false, authz.RoleAdmin, 1, authz.RoleViewer},
		{"member", false, authz.RoleAdmin, 2, authz.RoleMember},
		{"owner", false, authz.RoleAdmin, 3, authz.RoleOwner},
		{"owner capped to read", false, authz.RoleViewer, 3, authz.RoleViewer},
		{"owner capped to write", false, authz.RoleMember, 3, authz.RoleMember},
		{"viewer is not lifted by manage", false, authz.RoleAdmin, 1, authz.RoleViewer},
		{"admin without group role", true, authz.RoleAdmin, 0, authz.RoleAdmin},
		{"admin capped to read", true, authz.RoleViewer, 0, authz.RoleViewer},
		{"admin capped to write", true, authz.RoleMember, 3, authz.RoleMember},
		{"zero ceiling grants nothing", true, authz.RoleNone, 3, authz.RoleNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := authz.Principal{Kind: authz.KindSession, UserID: 1, IsAdmin: tc.admin, Ceiling: tc.ceiling}
			if got := authz.UserRole(p, tc.rank); got != tc.want {
				t.Errorf("UserRole() = %s, want %s", got, tc.want)
			}
		})
	}
}
