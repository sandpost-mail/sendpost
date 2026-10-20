package api

import "net/http"

type nameInput struct {
	Name string `json:"name"`
}

func (s *server) listGroups(w http.ResponseWriter, r *http.Request, _ *call) {
	list, err := s.Groups.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) createGroup(w http.ResponseWriter, r *http.Request, _ *call) {
	var in nameInput
	if !decodeBody(w, r, &in) {
		return
	}
	g, err := s.Groups.Create(r.Context(), in.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *server) getGroup(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	g, err := s.Groups.Get(r.Context(), gid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) renameGroup(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	var in nameInput
	if !decodeBody(w, r, &in) {
		return
	}
	g, err := s.Groups.Rename(r.Context(), gid, in.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) deleteGroup(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	if err := s.Groups.Delete(r.Context(), gid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listMembers(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	list, err := s.Groups.Members(r.Context(), gid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) addMember(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	if err := s.Groups.AddMember(r.Context(), gid, uid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) removeMember(w http.ResponseWriter, r *http.Request, _ *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	if err := s.Groups.RemoveMember(r.Context(), gid, uid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listProjectGroups(w http.ResponseWriter, r *http.Request, c *call) {
	list, err := s.Groups.ProjectAssignments(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) assignProjectGroup(w http.ResponseWriter, r *http.Request, c *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Groups.AssignRole(r.Context(), c.scope, gid, in.Role); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) removeProjectGroup(w http.ResponseWriter, r *http.Request, c *call) {
	gid, ok := pathID(w, r, "gid")
	if !ok {
		return
	}
	if err := s.Groups.RemoveRole(r.Context(), c.scope, gid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
