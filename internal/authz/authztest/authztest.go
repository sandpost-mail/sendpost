// Package authztest builds authz values for tests. It must not be imported by
// production code: Scope panics outside a test binary.
package authztest

import (
	"context"
	"fmt"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

// Scope returns an owner Scope on projectID without touching the database,
// for test setup that needs project access established by other means. It
// goes through authz.Authorizer.Project with a principal pinned to the
// project, so authz keeps no unchecked Scope constructor. projectID must not
// be zero.
func Scope(projectID int64) authz.Scope {
	if !testing.Testing() {
		panic("authztest: used outside a test binary")
	}
	p := authz.Principal{Kind: authz.KindProjectToken, ProjectID: projectID, Ceiling: authz.RoleOwner}
	// A project token principal resolves its role without a query, so no
	// database is needed.
	scope, err := authz.New(nil).Project(context.Background(), p, projectID, authz.CapManageProject)
	if err != nil {
		panic(fmt.Sprintf("authztest: scope for project %d: %v", projectID, err))
	}
	return scope
}
