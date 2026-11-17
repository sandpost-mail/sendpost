package api

import (
	"bytes"
	"encoding/json"
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

// compatJSON decodes the optional JSON body the guard buffered. empty is true
// when the body is blank. Unlike Mailpit, unknown fields and trailing data are
// refused: a mistyped key must not turn into "no IDs", which for DELETE
// /messages means "delete everything". Keys match without regard to case, as
// with encoding/json.
func compatJSON(c *compatCall, dst any) (empty bool, err error) {
	if len(bytes.TrimSpace(c.body)) == 0 {
		return true, nil
	}
	return false, decodeStrict(c.body, dst)
}

// compatBody decodes a required JSON body or answers 400.
func compatBody(w http.ResponseWriter, c *compatCall, dst any) bool {
	empty, err := compatJSON(c, dst)
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
	page, err := s.Messages.ListPage(r.Context(), c.scope, f, start, limit)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	tags, err := s.tagNames(r, c)
	if err != nil {
		s.compatFail(w, r, err)
		return
	}
	writeCompatJSON(w, mpList{
		Total: page.Mailbox.Total, Unread: page.Mailbox.Unread, Count: len(page.Rows),
		MessagesCount: page.Matches.Total, MessagesUnread: page.Matches.Unread,
		Start: start, Tags: tags, Messages: summariesOf(page.Rows),
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
	if !compatBody(w, c, &body) {
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
	if _, err := compatJSON(c, &body); err != nil {
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
