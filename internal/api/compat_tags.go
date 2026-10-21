package api

import (
	"net/http"
)

// tagNames lists the tag names in use in the project, never nil.
func (s *server) tagNames(r *http.Request, c *compatCall) ([]string, error) {
	tags, err := s.Messages.TagCounts(r.Context(), c.scope)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	return names, nil
}

// compatTags is GET /tags.
func (s *server) compatTags(w http.ResponseWriter, r *http.Request, c *compatCall) {
	names, err := s.tagNames(r, c)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	writeCompatJSON(w, names)
}

// compatSetTags is PUT /tags: replace the tags of the given messages.
func (s *server) compatSetTags(w http.ResponseWriter, r *http.Request, c *compatCall) {
	var body struct {
		Tags []string
		IDs  []string
	}
	if !compatBody(w, r, &body) {
		return
	}
	if len(body.IDs) > maxCompatIDs {
		compatText(w, http.StatusBadRequest, "too many IDs")
		return
	}
	if err := s.Messages.ReplaceTags(r.Context(), c.scope, parseIDs(body.IDs), body.Tags); err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}

// compatRenameTag is PUT /tags/{Tag}.
func (s *server) compatRenameTag(w http.ResponseWriter, r *http.Request, c *compatCall) {
	var body struct{ Name string }
	if !compatBody(w, r, &body) {
		return
	}
	if err := s.Messages.RenameTag(r.Context(), c.scope, r.PathValue("Tag"), body.Name); err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}

// compatDeleteTag is DELETE /tags/{Tag}: it removes the tag from its messages.
func (s *server) compatDeleteTag(w http.ResponseWriter, r *http.Request, c *compatCall) {
	if err := s.Messages.DeleteTag(r.Context(), c.scope, r.PathValue("Tag")); err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}
