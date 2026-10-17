package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/projects"
)

func (s *server) info(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, http.StatusOK, s.Info)
}

// listProjects returns what the caller may see: everything for instance
// administrators, the projects of the user's groups otherwise, and the single
// pinned project for a project token.
func (s *server) listProjects(w http.ResponseWriter, r *http.Request, c *call) {
	var (
		list []projects.Project
		err  error
	)
	switch {
	case c.p.Kind == authz.KindProjectToken:
		list, err = s.pinnedProject(r, c.p)
	case c.p.IsAdmin:
		list, err = s.Projects.List(r.Context())
	default:
		list, err = s.Projects.ListVisible(r.Context(), c.p.UserID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) pinnedProject(r *http.Request, p authz.Principal) ([]projects.Project, error) {
	scope, err := s.Authz.Project(r.Context(), p, p.ProjectID, authz.CapReadMessages)
	if err != nil {
		return nil, err
	}
	proj, err := s.Projects.Get(r.Context(), scope)
	if err != nil {
		return nil, err
	}
	return []projects.Project{proj}, nil
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request, _ *call) {
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
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request, c *call) {
	p, err := s.Projects.Get(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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
