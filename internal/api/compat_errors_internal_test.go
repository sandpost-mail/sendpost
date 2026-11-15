package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/authz/authztest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// errDB is the error of failingDB.
var errDB = errors.New("database is down")

// failingDB is a store.DB whose statements fail when fail(sql) is true and
// otherwise go to next (which may be nil when every statement fails).
type failingDB struct {
	next store.DB
	fail func(sql string) bool
}

func (d failingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if d.next == nil || d.fail(sql) {
		return pgconn.CommandTag{}, errDB
	}
	return d.next.Exec(ctx, sql, args...)
}

func (d failingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if d.next == nil || d.fail(sql) {
		return nil, errDB
	}
	return d.next.Query(ctx, sql, args...)
}

func (d failingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if d.next == nil || d.fail(sql) {
		return errRow{}
	}
	return d.next.QueryRow(ctx, sql, args...)
}

func (d failingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if d.next == nil {
		return nil, errDB
	}
	return d.next.Begin(ctx)
}

func (d failingDB) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	if d.next == nil {
		return 0, errDB
	}
	return d.next.CopyFrom(ctx, table, cols, src)
}

type errRow struct{}

func (errRow) Scan(...any) error { return errDB }

func always(string) bool { return true }

// isTagList matches the statement behind TagCounts.
func isTagList(sql string) bool { return strings.Contains(sql, "CompatListTags") }

// errorServer is a server on db that logs into the returned buffer.
func errorServer(db store.DB) (*server, *bytes.Buffer) {
	var logs bytes.Buffer
	return &server{Deps: Deps{
		Messages: messages.New(db),
		Log:      slog.New(slog.NewTextHandler(&logs, nil)),
	}}, &logs
}

func TestCompatFailMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		body   string
	}{
		{authz.ErrNotFound, http.StatusNotFound, "404 page not found"},
		{messages.ErrNotFound, http.StatusNotFound, "404 page not found"},
		{projects.ErrNotFound, http.StatusNotFound, "404 page not found"},
		{fmt.Errorf("rename: %w", messages.ErrTagNotFound), http.StatusNotFound, "404 page not found"},
		{authz.ErrForbidden, http.StatusForbidden, "forbidden"},
		{fmt.Errorf("release: %w", policy.ErrBlocked), http.StatusBadRequest, "release: " + policy.ErrBlocked.Error()},
		{search.ErrInvalidDate, http.StatusBadRequest, search.ErrInvalidDate.Error()},
		{search.ErrQueryTooLong, http.StatusBadRequest, search.ErrQueryTooLong.Error()},
		{search.ErrTooManyTerms, http.StatusBadRequest, search.ErrTooManyTerms.Error()},
		{search.ErrSizeTooLarge, http.StatusBadRequest, search.ErrSizeTooLarge.Error()},
		{messages.ErrEmptyFilter, http.StatusBadRequest, messages.ErrEmptyFilter.Error()},
		{messages.ErrTagExists, http.StatusBadRequest, messages.ErrTagExists.Error()},
		{messages.ErrInvalidTag, http.StatusBadRequest, messages.ErrInvalidTag.Error()},
		{errDB, http.StatusInternalServerError, "internal error"},
	}
	for _, tc := range tests {
		t.Run(tc.err.Error(), func(t *testing.T) {
			s, logs := errorServer(nil)
			w := httptest.NewRecorder()
			s.compatFail(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/messages", nil), tc.err)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Errorf("got %d %q, want %d %q", w.Code, w.Body, tc.status, tc.body)
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
				t.Errorf("content type %q", w.Header().Get("Content-Type"))
			}
			logged := strings.Contains(logs.String(), "compat request failed")
			if logged != (tc.status == http.StatusInternalServerError) {
				t.Errorf("logged=%v for status %d: %s", logged, tc.status, logs)
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(w.Body.String(), errDB.Error()) {
				t.Error("the 500 body leaks the error text")
			}
		})
	}
}

// Every database failure of the list, search and info handlers is a logged,
// generic 500, never a 200 with partial counts.
func TestCompatReadHandlersFail(t *testing.T) {
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "errors")
	handlers := map[string]func(*server, http.ResponseWriter, *http.Request, *compatCall){
		"list":   (*server).compatList,
		"search": (*server).compatSearch,
		"info":   (*server).compatInfo,
		"tags":   (*server).compatTags,
	}
	dbs := map[string]store.DB{
		"every query fails": failingDB{fail: always},
		"tag listing fails": failingDB{next: pool, fail: isTagList},
	}
	for dbName, db := range dbs {
		for name, h := range handlers {
			t.Run(dbName+"/"+name, func(t *testing.T) {
				s, logs := errorServer(db)
				w := httptest.NewRecorder()
				r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/x?query=subject:hello", nil)
				h(s, w, r, &compatCall{scope: authztest.Scope(pid), base: "/api/v1"})
				if w.Code != http.StatusInternalServerError || w.Body.String() != "internal error" {
					t.Errorf("got %d %q", w.Code, w.Body)
				}
				if !strings.Contains(logs.String(), errDB.Error()) {
					t.Errorf("the cause is not logged: %s", logs)
				}
			})
		}
	}
}
