// Package authz is the single place that decides who may do what. It holds
// the role matrix, resolves a principal's effective role on a project and
// hands out Scope values, the only way services reach project data.
package authz

import "fmt"

// Role is a level of access. The zero value grants nothing.
type Role int

// Roles in increasing order of power. RoleAdmin is the instance administrator,
// which acts as owner on every project and can manage global resources.
const (
	RoleNone Role = iota
	RoleViewer
	RoleMember
	RoleOwner
	RoleAdmin
)

// ParseRole converts a project role name (viewer, member, owner) to a Role.
// Admin is never a project role, so it is not accepted.
func ParseRole(s string) (Role, bool) {
	switch s {
	case "viewer":
		return RoleViewer, true
	case "member":
		return RoleMember, true
	case "owner":
		return RoleOwner, true
	}
	return RoleNone, false
}

// String returns the role name used in the API and the database.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleMember:
		return "member"
	case RoleOwner:
		return "owner"
	case RoleAdmin:
		return "admin"
	}
	return "none"
}

// ProjectRoleName is the role as the API presents it on a project: viewer,
// member or owner. Instance administrators act as owners, so admin maps to
// owner. It returns the empty string for no access.
func (r Role) ProjectRoleName() string {
	switch r {
	case RoleViewer, RoleMember:
		return r.String()
	case RoleOwner, RoleAdmin:
		return RoleOwner.String()
	}
	return ""
}

// Capability is one thing a principal may be allowed to do.
type Capability string

// Project capabilities depend on the principal's role on that project.
const (
	// CapReadMessages covers reading messages, attachments, the event stream
	// and marking a message read.
	CapReadMessages Capability = "messages.read"
	// CapWriteMessages covers delete, tag and comment.
	CapWriteMessages Capability = "messages.write"
	// CapManageProject covers credentials, tag rules, policy, transport and
	// project API tokens.
	CapManageProject Capability = "project.manage"
	// CapManageProjectGroups covers role assignment of groups on a project and
	// reading the project audit log.
	CapManageProjectGroups Capability = "project.groups"
)

// Global capabilities are only held by instance administrators.
const (
	CapManageUsers   Capability = "users.manage"
	CapManageGroups  Capability = "groups.manage"
	CapCreateProject Capability = "projects.create"
	CapReadAudit     Capability = "audit.read"
)

// matrix maps each capability to the minimum role that holds it. It mirrors
// the table in docs/ARCHITECTURE.md section 2.5. A capability that is missing
// from the matrix is held by nobody, so a typo fails closed.
var matrix = map[Capability]Role{
	CapReadMessages:        RoleViewer,
	CapWriteMessages:       RoleMember,
	CapManageProject:       RoleOwner,
	CapManageProjectGroups: RoleOwner,
	CapManageUsers:         RoleAdmin,
	CapManageGroups:        RoleAdmin,
	CapCreateProject:       RoleAdmin,
	CapReadAudit:           RoleAdmin,
}

// Allows reports whether the role holds the capability.
func (r Role) Allows(c Capability) bool {
	need, ok := matrix[c]
	return ok && r >= need && r != RoleNone
}

// Capabilities returns every capability in the matrix, for tests and docs.
func Capabilities() []Capability {
	out := make([]Capability, 0, len(matrix))
	for c := range matrix {
		out = append(out, c)
	}
	return out
}

// MinRole returns the lowest role holding c.
func MinRole(c Capability) (Role, error) {
	r, ok := matrix[c]
	if !ok {
		return RoleNone, fmt.Errorf("unknown capability %q", c)
	}
	return r, nil
}

// ScopeRole is the role ceiling an API token scope grants. read is viewer,
// write is member, manage lifts the ceiling to whatever the owning user holds.
func ScopeRole(scope string) (Role, bool) {
	switch scope {
	case "read":
		return RoleViewer, true
	case "write":
		return RoleMember, true
	case "manage":
		return RoleAdmin, true
	}
	return RoleNone, false
}

// CeilingOf returns the highest role granted by a set of token scopes, and
// false when a scope is unknown or the set is empty.
func CeilingOf(scopes []string) (Role, bool) {
	ceiling := RoleNone
	for _, s := range scopes {
		r, ok := ScopeRole(s)
		if !ok {
			return RoleNone, false
		}
		ceiling = max(ceiling, r)
	}
	return ceiling, ceiling != RoleNone
}
