package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/projects"
)

func (s *server) info(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, http.StatusOK, s.Info)
}

// projectView is a project with the caller's effective role on it.
type projectView struct {
	projects.Project
	// Role is viewer, member or owner. Instance administrators are owners.
	// A token caller gets the role capped by its scopes.
	Role string `json:"role"`
}

func viewOf(p projects.Project, role authz.Role) projectView {
	return projectView{Project: p, Role: role.ProjectRoleName()}
}

// listProjects returns what the caller may see: everything for instance
// administrators, the projects of the user's groups otherwise, and the single
// pinned project for a project token. Each carries the caller's effective
// role, resolved with the same rules as the guard and without a query per
// project.
func (s *server) listProjects(w http.ResponseWriter, r *http.Request, c *call) {
	var (
		list []projectView
		err  error
	)
	switch {
	case c.p.Kind == authz.KindProjectToken:
		list, err = s.pinnedProject(r, c.p)
	case c.p.IsAdmin:
		list, err = s.allProjects(r, c.p)
	default:
		list, err = s.visibleProjects(r, c.p)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) allProjects(r *http.Request, p authz.Principal) ([]projectView, error) {
	all, err := s.Projects.List(r.Context())
	if err != nil {
		return nil, err
	}
	role := authz.UserRole(p, 0) // administrators hold admin on everything, capped by the token
	out := make([]projectView, 0, len(all))
	for _, proj := range all {
		out = append(out, viewOf(proj, role))
	}
	return out, nil
}

func (s *server) visibleProjects(r *http.Request, p authz.Principal) ([]projectView, error) {
	visible, err := s.Projects.ListVisible(r.Context(), p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]projectView, 0, len(visible))
	for _, v := range visible {
		if role := authz.UserRole(p, v.Rank); role != authz.RoleNone {
			out = append(out, viewOf(v.Project, role))
		}
	}
	return out, nil
}

func (s *server) pinnedProject(r *http.Request, p authz.Principal) ([]projectView, error) {
	scope, err := s.Authz.Project(r.Context(), p, p.ProjectID, authz.CapReadMessages)
	if err != nil {
		return nil, err
	}
	proj, err := s.Projects.Get(r.Context(), scope)
	if err != nil {
		return nil, err
	}
	return []projectView{viewOf(proj, scope.Role())}, nil
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request, c *call) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	p, err := s.Projects.Create(r.Context(), in.Name, in.Slug)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Only administrators get here, they own what they create.
	writeJSON(w, http.StatusCreated, viewOf(p, authz.UserRole(c.p, 0)))
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request, c *call) {
	p, err := s.Projects.Get(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(p, c.scope.Role()))
}

func (s *server) listTagRules(w http.ResponseWriter, r *http.Request, c *call) {
	rules, err := s.Projects.ListTagRules(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rules})
}

func (s *server) createTagRule(w http.ResponseWriter, r *http.Request, c *call) {
	var in projects.TagRuleInput
	if !decodeBody(w, r, &in) {
		return
	}
	rule, err := s.Projects.CreateTagRule(r.Context(), c.scope, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *server) deleteTagRule(w http.ResponseWriter, r *http.Request, c *call) {
	rid, ok := pathID(w, r, "rid")
	if !ok {
		return
	}
	if err := s.Projects.DeleteTagRule(r.Context(), c.scope, rid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
