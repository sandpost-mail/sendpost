package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Errors returned by the Authorizer.
var (
	// ErrNotFound means the project does not exist or the principal has no
	// role on it. The two cases are deliberately indistinguishable, so ids
	// can not be probed.
	ErrNotFound = errors.New("not found")
	// ErrForbidden means the principal can see the resource but lacks the
	// capability.
	ErrForbidden = errors.New("forbidden")
)

// Scope is proof that a principal passed the capability check for one
// project. Services that read or write project data take a Scope instead of a
// bare project id. The only constructors are Authorizer.Project, Verify on a
// signed grant, and Trusted.
type Scope struct {
	projectID int64
	role      Role
}

// ProjectID returns the project the scope is bound to.
func (s Scope) ProjectID() int64 { return s.projectID }

// Role returns the effective role the scope was granted with.
func (s Scope) Role() Role { return s.role }

// Trusted builds a Scope without a principal check. Use it only where access
// was established by other means, for example the SMTP credential that fixes
// the project of a session, or test setup. Handlers must not call it.
func Trusted(projectID int64) Scope { return Scope{projectID: projectID, role: RoleOwner} }

// Authorizer resolves roles from the database.
type Authorizer struct {
	q *sqlcgen.Queries
}

// New returns an Authorizer on db.
func New(db sqlcgen.DBTX) *Authorizer { return &Authorizer{q: sqlcgen.New(db)} }

// RoleOn returns the principal's effective role on a project: the highest
// role among the user's groups assigned to it (instance admins are admin),
// capped by the token ceiling. It returns ErrNotFound when the project does
// not exist or the principal has no access to it.
func (a *Authorizer) RoleOn(ctx context.Context, p Principal, projectID int64) (Role, error) {
	base, err := a.baseRole(ctx, p, projectID)
	if err != nil {
		return RoleNone, err
	}
	eff := min(base, p.Ceiling)
	if eff == RoleNone {
		return RoleNone, ErrNotFound
	}
	return eff, nil
}

func (a *Authorizer) baseRole(ctx context.Context, p Principal, projectID int64) (Role, error) {
	if p.Kind == KindProjectToken {
		if p.ProjectID == 0 || p.ProjectID != projectID {
			return RoleNone, ErrNotFound
		}
		return RoleAdmin, nil // the ceiling limits it
	}
	if p.UserID == 0 {
		return RoleNone, ErrNotFound
	}
	rank, err := a.q.ProjectRoleRank(ctx, sqlcgen.ProjectRoleRankParams{UserID: p.UserID, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleNone, ErrNotFound
	}
	if err != nil {
		return RoleNone, fmt.Errorf("project role: %w", err)
	}
	if p.IsAdmin {
		return RoleAdmin, nil
	}
	return Role(rank), nil // rank 1..3 matches viewer..owner, 0 is none
}

// Project checks that the principal holds c on the project and returns the
// Scope for it. Errors: ErrNotFound (no access or no such project) and
// ErrForbidden (visible but not allowed).
func (a *Authorizer) Project(ctx context.Context, p Principal, projectID int64, c Capability) (Scope, error) {
	role, err := a.RoleOn(ctx, p, projectID)
	if err != nil {
		return Scope{}, err
	}
	if !role.Allows(c) {
		return Scope{}, ErrForbidden
	}
	return Scope{projectID: projectID, role: role}, nil
}

// Global checks a global capability. Project tokens never hold one, and a
// user token needs the manage scope.
func Global(p Principal, c Capability) error {
	role := RoleNone
	if p.IsUser() && p.IsAdmin {
		role = min(RoleAdmin, p.Ceiling)
	}
	if !role.Allows(c) {
		return ErrForbidden
	}
	return nil
}
