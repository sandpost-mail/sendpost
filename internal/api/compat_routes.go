package api

import (
	"net/http"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

// The Mailpit compatible API is served at /api/v1 (project tokens) and at
// /p/{slug}/api/v1 (any token with a role on the project). Its contract is
// the vendored Mailpit swagger file, see docs/MAILPIT_COMPATIBILITY.md, which
// is a separate document from docs/openapi.yaml (the native API).
const (
	// CompatPrefix is the base path of the Mailpit compatible API.
	CompatPrefix = "/api/v1"
	// compatAliasPrefix is the explicit-project form of it, with the slug in front.
	compatAliasPrefix = "/p/{slug}"
)

type compatRoute struct {
	method string
	// path is relative to CompatPrefix and written like the Mailpit swagger,
	// except for /view pages which are served outside the prefix.
	path string
	// muxPath replaces path when registering, for the /view pages whose
	// "{ID}.html" can not be a ServeMux pattern.
	muxPath string
	cap     authz.Capability
	h       compatHandler
	// unsupported routes answer 501 once authenticated.
	unsupported bool
}

// CompatRouteInfo describes one route of the compatible API, for tests and
// documentation tooling.
type CompatRouteInfo struct {
	Method     string
	Path       string // as in the Mailpit swagger, "/api/v1" included
	Capability string
	Supported  bool
}

// CompatRoutes lists every route of the Mailpit compatible API, including the
// ones that answer 501.
func CompatRoutes() []CompatRouteInfo {
	s := &server{}
	table := s.compatRoutes()
	out := make([]CompatRouteInfo, 0, len(table))
	for _, rt := range table {
		out = append(out, CompatRouteInfo{
			Method: rt.method, Path: rt.fullPath(), Capability: string(rt.cap), Supported: !rt.unsupported,
		})
	}
	return out
}

// fullPath is the path as the Mailpit swagger spells it.
func (rt compatRoute) fullPath() string {
	if strings.HasPrefix(rt.path, "/view/") {
		return rt.path
	}
	return CompatPrefix + rt.path
}

// patterns returns the ServeMux patterns of the route: the plain form and the
// /p/{slug} form.
func (rt compatRoute) patterns() (plain, alias string) {
	path := rt.fullPath()
	if rt.muxPath != "" {
		path = rt.muxPath
	}
	return rt.method + " " + path, rt.method + " " + compatAliasPrefix + path
}

func (s *server) registerCompat(mux *http.ServeMux) {
	notFound := func(w http.ResponseWriter, _ *http.Request) { compatNotFound(w) }
	mux.HandleFunc(CompatPrefix+"/", notFound)
	mux.HandleFunc(compatAliasPrefix+CompatPrefix+"/", notFound)
	registered := map[string]bool{}
	for _, rt := range s.compatRoutes() {
		plain, alias := rt.patterns()
		if registered[plain] {
			continue // the html and txt views share one pattern
		}
		registered[plain] = true
		mux.Handle(plain, s.compatGuard(rt, false))
		mux.Handle(alias, s.compatGuard(rt, true))
	}
}

func (s *server) unsupported(method, path string) compatRoute {
	return compatRoute{method: method, path: path, cap: authz.CapReadMessages, h: s.compatUnsupported, unsupported: true}
}

// compatRoutes is the single declaration of the compatible API. Reads need
// messages.read, deleting and tag changes need messages.write. Marking read
// is a read, as in the native API.
func (s *server) compatRoutes() []compatRoute {
	const (
		read  = authz.CapReadMessages
		write = authz.CapWriteMessages
		view  = "/view/{name}"
	)
	return []compatRoute{
		{method: "GET", path: "/info", cap: read, h: s.compatInfo},
		s.unsupported("GET", "/webui"),
		s.unsupported("GET", "/chaos"),
		s.unsupported("PUT", "/chaos"),
		s.unsupported("POST", "/send"),

		{method: "GET", path: "/messages", cap: read, h: s.compatList},
		{method: "PUT", path: "/messages", cap: read, h: s.compatSetRead},
		{method: "DELETE", path: "/messages", cap: write, h: s.compatDelete},
		{method: "GET", path: "/search", cap: read, h: s.compatSearch},
		{method: "DELETE", path: "/search", cap: write, h: s.compatDeleteSearch},

		{method: "GET", path: "/message/{ID}", cap: read, h: s.compatMessage},
		{method: "GET", path: "/message/{ID}/headers", cap: read, h: s.compatHeaders},
		{method: "GET", path: "/message/{ID}/raw", cap: read, h: s.compatRaw},
		{method: "GET", path: "/message/{ID}/part/{PartID}", cap: read, h: s.compatPart},
		s.unsupported("GET", "/message/{ID}/part/{PartID}/thumb"),
		s.unsupported("GET", "/message/{ID}/html-check"),
		s.unsupported("GET", "/message/{ID}/link-check"),
		s.unsupported("GET", "/message/{ID}/sa-check"),
		s.unsupported("POST", "/message/{ID}/release"),

		{method: "GET", path: "/tags", cap: read, h: s.compatTags},
		{method: "PUT", path: "/tags", cap: write, h: s.compatSetTags},
		{method: "PUT", path: "/tags/{Tag}", cap: write, h: s.compatRenameTag},
		{method: "DELETE", path: "/tags/{Tag}", cap: write, h: s.compatDeleteTag},

		{method: "GET", path: "/view/{ID}.html", muxPath: view, cap: read, h: s.compatView},
		{method: "GET", path: "/view/{ID}.txt", muxPath: view, cap: read, h: s.compatView},
	}
}

func (s *server) compatUnsupported(w http.ResponseWriter, _ *http.Request, _ *compatCall) {
	compatText(w, http.StatusNotImplemented, "not supported by Sandpost, see docs/MAILPIT_COMPATIBILITY.md")
}
