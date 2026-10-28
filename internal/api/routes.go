package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

// access says what a route requires. Every native API route is declared in
// the table below, and enforcement happens in one place (guard), so a handler
// can not forget to check access. The cross tenant leakage test and the
// OpenAPI check both iterate over this table.
type access int

const (
	// accessPublic needs nothing: login only.
	accessPublic access = iota
	// accessAny needs any authenticated principal, including project tokens.
	accessAny
	// accessUser needs a user principal (session or user token).
	accessUser
	// accessGlobal needs a global (instance administrator) capability.
	accessGlobal
	// accessProject needs a project capability on {pid}. A foreign or
	// unknown project answers 404, an insufficient role 403.
	accessProject
	// accessSigned is accessProject, or a signed grant bound to the exact
	// project, message and resource, for the sandboxed preview iframe.
	accessSigned
)

func (a access) String() string {
	return [...]string{"public", "any", "user", "global", "project", "signed"}[a]
}

// call is what a handler receives after the guard let the request through.
type call struct {
	// p is the authenticated principal, zero for public routes and signed grants.
	p authz.Principal
	// scope is set for accessProject and accessSigned routes.
	scope authz.Scope
}

type handler func(w http.ResponseWriter, r *http.Request, c *call)

type route struct {
	method string
	// path is relative to V1Prefix and uses OpenAPI style {params}.
	path   string
	access access
	cap    authz.Capability
	// allowPasswordChange lets a user with must_change_password through.
	allowPasswordChange bool
	// resource names what a signed grant covers (accessSigned only).
	resource func(r *http.Request) string
	h        handler
}

// RouteInfo describes one route, for tests and documentation tooling.
type RouteInfo struct {
	Method     string
	Path       string // relative to V1Prefix
	Access     string
	Capability string
}

// Routes lists every native API route.
func Routes() []RouteInfo {
	s := &server{}
	table := s.routes()
	out := make([]RouteInfo, 0, len(table))
	for _, rt := range table {
		out = append(out, RouteInfo{Method: rt.method, Path: rt.path, Access: rt.access.String(), Capability: string(rt.cap)})
	}
	return out
}

func (s *server) registerV1(mux *http.ServeMux) {
	mux.HandleFunc(V1Prefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	for _, rt := range s.routes() {
		mux.Handle(rt.method+" "+V1Prefix+rt.path, requireJSONWrites(s.guard(rt)))
	}
}

func htmlResource(*http.Request) string { return "html" }

func attachmentResource(r *http.Request) string { return "attachment/" + r.PathValue("ordinal") }

// routes is the single declaration of the native API.
func (s *server) routes() []route {
	const p = "/projects/{pid}"
	return []route{
		{method: "GET", path: "/info", access: accessAny, h: s.info},

		// Authentication.
		{method: "POST", path: "/auth/login", access: accessPublic, h: s.login},
		{method: "POST", path: "/auth/logout", access: accessAny, allowPasswordChange: true, h: s.logout},
		{method: "GET", path: "/auth/me", access: accessUser, allowPasswordChange: true, h: s.me},
		{method: "POST", path: "/auth/password", access: accessUser, allowPasswordChange: true, h: s.changePassword},

		// Own API tokens.
		{method: "GET", path: "/users/me/tokens", access: accessUser, h: s.listMyTokens},
		{method: "POST", path: "/users/me/tokens", access: accessUser, h: s.createMyToken},
		{method: "DELETE", path: "/users/me/tokens/{tid}", access: accessUser, h: s.revokeMyToken},

		// Users (instance administrators).
		{method: "GET", path: "/users", access: accessGlobal, cap: authz.CapManageUsers, h: s.listUsers},
		{method: "POST", path: "/users", access: accessGlobal, cap: authz.CapManageUsers, h: s.createUser},
		{method: "GET", path: "/users/{uid}", access: accessGlobal, cap: authz.CapManageUsers, h: s.getUser},
		{method: "PATCH", path: "/users/{uid}", access: accessGlobal, cap: authz.CapManageUsers, h: s.updateUser},
		{method: "POST", path: "/users/{uid}/password", access: accessGlobal, cap: authz.CapManageUsers, h: s.resetPassword},
		{method: "GET", path: "/users/{uid}/tokens", access: accessGlobal, cap: authz.CapManageUsers, h: s.listUserTokens},
		{method: "DELETE", path: "/users/{uid}/tokens/{tid}", access: accessGlobal, cap: authz.CapManageUsers, h: s.revokeUserToken},

		// Groups (instance administrators).
		{method: "GET", path: "/groups", access: accessGlobal, cap: authz.CapManageGroups, h: s.listGroups},
		{method: "POST", path: "/groups", access: accessGlobal, cap: authz.CapManageGroups, h: s.createGroup},
		{method: "GET", path: "/groups/{gid}", access: accessGlobal, cap: authz.CapManageGroups, h: s.getGroup},
		{method: "PATCH", path: "/groups/{gid}", access: accessGlobal, cap: authz.CapManageGroups, h: s.renameGroup},
		{method: "DELETE", path: "/groups/{gid}", access: accessGlobal, cap: authz.CapManageGroups, h: s.deleteGroup},
		{method: "GET", path: "/groups/{gid}/members", access: accessGlobal, cap: authz.CapManageGroups, h: s.listMembers},
		{method: "PUT", path: "/groups/{gid}/members/{uid}", access: accessGlobal, cap: authz.CapManageGroups, h: s.addMember},
		{method: "DELETE", path: "/groups/{gid}/members/{uid}", access: accessGlobal, cap: authz.CapManageGroups, h: s.removeMember},

		{method: "GET", path: "/audit", access: accessGlobal, cap: authz.CapReadAudit, h: s.listAudit},

		// Projects.
		{method: "GET", path: "/projects", access: accessAny, h: s.listProjects},
		{method: "POST", path: "/projects", access: accessGlobal, cap: authz.CapCreateProject, h: s.createProject},
		{method: "GET", path: p, access: accessProject, cap: authz.CapReadMessages, h: s.getProject},

		{method: "GET", path: p + "/groups", access: accessProject, cap: authz.CapManageProjectGroups, h: s.listProjectGroups},
		{method: "PUT", path: p + "/groups/{gid}", access: accessProject, cap: authz.CapManageProjectGroups, h: s.assignProjectGroup},
		{method: "DELETE", path: p + "/groups/{gid}", access: accessProject, cap: authz.CapManageProjectGroups, h: s.removeProjectGroup},
		{method: "GET", path: p + "/audit", access: accessProject, cap: authz.CapManageProjectGroups, h: s.listProjectAudit},

		{method: "GET", path: p + "/credentials", access: accessProject, cap: authz.CapManageProject, h: s.listCredentials},
		{method: "POST", path: p + "/credentials", access: accessProject, cap: authz.CapManageProject, h: s.createCredential},
		{method: "DELETE", path: p + "/credentials/{cid}", access: accessProject, cap: authz.CapManageProject, h: s.revokeCredential},

		{method: "GET", path: p + "/tokens", access: accessProject, cap: authz.CapManageProject, h: s.listProjectTokens},
		{method: "POST", path: p + "/tokens", access: accessProject, cap: authz.CapManageProject, h: s.createProjectToken},
		{method: "DELETE", path: p + "/tokens/{tid}", access: accessProject, cap: authz.CapManageProject, h: s.revokeProjectToken},

		{method: "GET", path: p + "/tag-rules", access: accessProject, cap: authz.CapReadMessages, h: s.listTagRules},
		{method: "POST", path: p + "/tag-rules", access: accessProject, cap: authz.CapManageProject, h: s.createTagRule},
		{method: "DELETE", path: p + "/tag-rules/{rid}", access: accessProject, cap: authz.CapManageProject, h: s.deleteTagRule},

		{method: "GET", path: p + "/transport", access: accessProject, cap: authz.CapManageProject, h: s.getTransport},
		{method: "PUT", path: p + "/transport", access: accessProject, cap: authz.CapManageProject, h: s.putTransport},
		{method: "DELETE", path: p + "/transport", access: accessProject, cap: authz.CapManageProject, h: s.deleteTransport},
		{method: "POST", path: p + "/transport/test", access: accessProject, cap: authz.CapManageProject, h: s.testTransport},
		{method: "GET", path: p + "/policy", access: accessProject, cap: authz.CapManageProject, h: s.getPolicy},
		{method: "PUT", path: p + "/policy", access: accessProject, cap: authz.CapManageProject, h: s.putPolicy},

		{method: "GET", path: p + "/messages", access: accessProject, cap: authz.CapReadMessages, h: s.listMessages},
		{method: "GET", path: p + "/messages/{mid}", access: accessProject, cap: authz.CapReadMessages, h: s.getMessage},
		{method: "POST", path: p + "/messages/{mid}/read", access: accessProject, cap: authz.CapReadMessages, h: s.markRead},
		{method: "GET", path: p + "/messages/{mid}/deliveries", access: accessProject, cap: authz.CapReadMessages, h: s.listDeliveries},
		{method: "POST", path: p + "/messages/{mid}/release", access: accessProject, cap: authz.CapWriteMessages, h: s.releaseMessage},
		{method: "GET", path: p + "/messages/{mid}/comments", access: accessProject, cap: authz.CapReadMessages, h: s.listComments},
		{method: "POST", path: p + "/messages/{mid}/comments", access: accessProject, cap: authz.CapWriteMessages, h: s.createComment},
		{method: "GET", path: p + "/activity", access: accessProject, cap: authz.CapReadMessages, h: s.listActivity},
		{method: "GET", path: p + "/messages/{mid}/raw", access: accessProject, cap: authz.CapReadMessages, h: s.rawMessage},
		{method: "GET", path: p + "/messages/{mid}/preview-url", access: accessProject, cap: authz.CapReadMessages, h: s.previewURL},
		{method: "GET", path: p + "/messages/{mid}/html", access: accessSigned, cap: authz.CapReadMessages, resource: htmlResource, h: s.htmlMessage},
		{method: "GET", path: p + "/messages/{mid}/attachments/{ordinal}", access: accessSigned, cap: authz.CapReadMessages, resource: attachmentResource, h: s.attachment},
		{method: "GET", path: p + "/messages/{mid}/extract", access: accessProject, cap: authz.CapReadMessages, h: s.extractMessage},
		{method: "GET", path: p + "/wait", access: accessProject, cap: authz.CapReadMessages, h: s.waitMessages},
		{method: "GET", path: p + "/events", access: accessProject, cap: authz.CapReadMessages, h: s.streamEvents},
	}
}
