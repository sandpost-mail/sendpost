package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/projects"
)

func (s *server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Info)
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Projects.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request) {
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

func (s *server) getProject(w http.ResponseWriter, r *http.Request, pid int64) {
	p, err := s.Projects.Get(r.Context(), pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) listTagRules(w http.ResponseWriter, r *http.Request, pid int64) {
	rules, err := s.Projects.ListTagRules(r.Context(), pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rules})
}

func (s *server) createTagRule(w http.ResponseWriter, r *http.Request, pid int64) {
	var in projects.TagRuleInput
	if !decodeBody(w, r, &in) {
		return
	}
	rule, err := s.Projects.CreateTagRule(r.Context(), pid, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *server) deleteTagRule(w http.ResponseWriter, r *http.Request, pid int64) {
	rid, ok := pathID(w, r, "rid")
	if !ok {
		return
	}
	if err := s.Projects.DeleteTagRule(r.Context(), pid, rid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
