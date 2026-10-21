package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

const (
	compatDefaultLimit = 50
	maxCompatIDs       = 10_000
)

// writeCompatJSON writes v as JSON, the way Mailpit does.
func writeCompatJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// compatPaging reads start and limit like Mailpit: junk falls back to the
// defaults (0 and 50), and limit=0 means "everything", capped here.
func compatPaging(r *http.Request) (start, limit int) {
	limit = compatDefaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("start")); err == nil && n > 0 {
		start = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 0 {
		limit = n
	}
	if limit == 0 {
		limit = messages.MaxCompatLimit
	}
	return start, limit
}

// compatFilter parses a Mailpit search string, with the tz parameter for
// before: and after:.
func compatFilter(r *http.Request, q string) (search.Filter, error) {
	return search.ParseMailpit(q, search.LoadLocation(r.URL.Query().Get("tz")))
}

// parseIDs converts Mailpit ids. Text that is not an id is dropped: it can
// not match a message, the same as an id that does not exist.
func parseIDs(in []string) []int64 {
	out := make([]int64, 0, len(in))
	for _, s := range in {
		if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// readCompatJSON decodes an optional JSON body. empty is true when there is
// no body at all. Unknown fields are ignored, as Mailpit does.
func readCompatJSON(r *http.Request, dst any) (empty bool, err error) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return true, nil
	}
	return false, json.Unmarshal(data, dst)
}

// compatBody decodes a required JSON body or answers 400.
func compatBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	empty, err := readCompatJSON(r, dst)
	if err != nil || empty {
		compatText(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (s *server) compatList(w http.ResponseWriter, r *http.Request, c *compatCall) {
	start, limit := compatPaging(r)
	s.writeCompatList(w, r, c, search.Filter{}, start, limit)
}

func (s *server) compatSearch(w http.ResponseWriter, r *http.Request, c *compatCall) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	if q == "" {
		compatText(w, http.StatusBadRequest, "Error: no search query")
		return
	}
	f, err := compatFilter(r, q)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	start, limit := compatPaging(r)
	s.writeCompatList(w, r, c, f, start, limit)
}

// writeCompatList answers a list or search request. total and unread describe
// the whole mailbox, messages_count and messages_unread the matches.
func (s *server) writeCompatList(w http.ResponseWriter, r *http.Request, c *compatCall, f search.Filter, start, limit int) {
	ctx := r.Context()
	rows, err := s.Messages.ListFiltered(ctx, c.scope, f, start, limit)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	mailbox, err := s.Messages.CountFiltered(ctx, c.scope, search.Filter{})
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	matches := mailbox
	if !f.IsZero() {
		if matches, err = s.Messages.CountFiltered(ctx, c.scope, f); err != nil {
			s.compatFail(w, r, err)
			return
		}
	}
	tags, err := s.tagNames(r, c)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	writeCompatJSON(w, mpList{
		Total: mailbox.Total, Unread: mailbox.Unread, Count: len(rows),
		MessagesCount: matches.Total, MessagesUnread: matches.Unread,
		Start: start, Tags: tags, Messages: summariesOf(rows),
	})
}

// compatSetRead is PUT /messages: mark messages read or unread by ids, by
// search, or all of them.
func (s *server) compatSetRead(w http.ResponseWriter, r *http.Request, c *compatCall) {
	var body struct {
		Read   bool
		IDs    []string
		Search string
	}
	if !compatBody(w, r, &body) {
		return
	}
	if len(body.IDs) > maxCompatIDs {
		compatText(w, http.StatusBadRequest, "too many IDs")
		return
	}
	var err error
	switch {
	case len(body.IDs) > 0 && body.Search != "":
		compatText(w, http.StatusBadRequest, "You may specify either IDs or a search query, not both")
		return
	case body.Search != "":
		var f search.Filter
		if f, err = compatFilter(r, body.Search); err == nil {
			err = s.Messages.SetReadFiltered(r.Context(), c.scope, f, body.Read)
		}
	case len(body.IDs) == 0:
		err = s.Messages.SetReadAll(r.Context(), c.scope, body.Read)
	default:
		err = s.Messages.SetReadIDs(r.Context(), c.scope, parseIDs(body.IDs), body.Read)
	}
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}

// compatDelete is DELETE /messages: delete the given ids, or every message
// when there are none, as Mailpit does. A body that is not JSON is refused
// instead of deleting everything.
func (s *server) compatDelete(w http.ResponseWriter, r *http.Request, c *compatCall) {
	var body struct{ IDs []string }
	if _, err := readCompatJSON(r, &body); err != nil {
		compatText(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.IDs) > maxCompatIDs {
		compatText(w, http.StatusBadRequest, "too many IDs")
		return
	}
	var err error
	if len(body.IDs) == 0 {
		_, err = s.Messages.DeleteAll(r.Context(), c.scope)
	} else {
		_, err = s.Messages.DeleteIDs(r.Context(), c.scope, parseIDs(body.IDs))
	}
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}

// compatDeleteSearch is DELETE /search. A query without any usable term is
// refused, so a typo can not turn into "delete everything".
func (s *server) compatDeleteSearch(w http.ResponseWriter, r *http.Request, c *compatCall) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	if q == "" {
		compatText(w, http.StatusBadRequest, "Error: no search query")
		return
	}
	f, err := compatFilter(r, q)
	if err == nil {
		_, err = s.Messages.DeleteFiltered(r.Context(), c.scope, f)
	}
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	compatOK(w)
}
