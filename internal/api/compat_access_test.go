package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

// compatPath fills the swagger style parameters of a route path.
func compatPath(rt api.CompatRouteInfo, t tenant) string {
	return strings.NewReplacer(
		"{ID}", itoa64(t.mid), "{PartID}", "0", "{Tag}", "t", "{name}", itoa64(t.mid)+".html",
	).Replace(rt.Path)
}

func compatBodyFor(rt api.CompatRouteInfo) string {
	switch rt.Method + " " + rt.Path {
	case "PUT /api/v1/messages":
		return `{"Read":true}`
	case "PUT /api/v1/tags":
		return `{"IDs":[],"Tags":[]}`
	case "PUT /api/v1/tags/{Tag}":
		return `{"Name":"x"}`
	}
	return ""
}

func TestCompatUnauthenticatedRoutesAreRejected(t *testing.T) {
	w := newWorld(t)
	for _, rt := range api.CompatRoutes() {
		t.Run(rt.Method+" "+rt.Path, func(t *testing.T) {
			for _, prefix := range []string{"", "/p/one"} {
				path := prefix + compatPath(rt, w.p1)
				r := w.a.compat(authNone, "", rt.Method, path, compatBodyFor(rt))
				if r.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s: status %d, want 401: %s", path, r.StatusCode, r.body)
				}
				// A browser session must not work here, even with its CSRF header.
				req, _ := http.NewRequestWithContext(t.Context(), rt.Method, w.a.srv.URL+path, nil)
				req.AddCookie(&http.Cookie{Name: "sandpost_session", Value: "x"})
				req.AddCookie(&http.Cookie{Name: "__Host-sandpost_session", Value: "x"})
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s with a cookie: status %d, want 401", path, resp.StatusCode)
				}
			}
		})
	}
}

// TestCompatRoleMatrix runs every route as principals with different power on
// project one, through the alias, and checks the capability the route needs.
func TestCompatRoleMatrix(t *testing.T) {
	w := newWorld(t)
	principals := []struct {
		name  string
		token string
		read  bool // may use routes that need messages.read
		write bool // may use routes that need messages.write
	}{
		{"viewer", w.vera, true, false},
		{"member", w.alice, true, true},
		{"owner", w.owner1, true, true},
		{"owner limited to scope read", w.ownerRead, true, false},
		{"administrator limited to scope read", w.adminRead, true, false},
		{"administrator", w.a.token, true, true},
		{"project token read", w.proj1Read, true, false},
		{"project token write", w.proj1Write, true, true},
	}
	for _, rt := range api.CompatRoutes() {
		for _, p := range principals {
			t.Run(rt.Method+" "+rt.Path+" as "+p.name, func(t *testing.T) {
				allowed := p.read
				if rt.Capability == string(authz.CapWriteMessages) {
					allowed = p.write
				}
				// Project tokens use the bare path, users the alias.
				prefix := "/p/one"
				if strings.HasPrefix(p.name, "project token") {
					prefix = ""
				}
				r := w.a.mp(p.token, rt.Method, prefix+compatPath(rt, w.p1), compatBodyFor(rt))
				if allowed && (r.StatusCode == http.StatusForbidden || r.StatusCode == http.StatusUnauthorized) {
					t.Errorf("should be allowed: status %d: %s", r.StatusCode, r.body)
				}
				if !allowed && r.StatusCode != http.StatusForbidden {
					t.Errorf("should be forbidden: status %d: %s", r.StatusCode, r.body)
				}
			})
		}
	}
}

// TestCompatForeignProjectsLookMissing checks that no way of addressing a
// project the caller has no role on differs from addressing one that does not
// exist.
func TestCompatForeignProjectsLookMissing(t *testing.T) {
	w := newWorld(t)
	missing := w.a.mp(w.alice, "GET", "/p/no-such-project/api/v1/messages", "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing project: %d", missing.StatusCode)
	}
	tests := []struct {
		name, token, path string
	}{
		{"user without a role", w.nobody, "/p/one/api/v1/messages"},
		{"user of another project", w.bob, "/p/one/api/v1/messages"},
		{"user on the project of someone else", w.alice, "/p/two/api/v1/messages"},
		{"project token on another slug", w.proj1Write, "/p/two/api/v1/messages"},
		{"project token on another slug, view", w.proj1Write, "/p/two/view/latest.html"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := w.a.mp(tc.token, "GET", tc.path, "")
			if r.StatusCode != missing.StatusCode || r.text() != missing.text() {
				t.Errorf("got %d %q, a missing project gives %d %q", r.StatusCode, r.body, missing.StatusCode, missing.body)
			}
			for k := range missing.Header {
				if k == "Date" || k == "Content-Length" {
					continue
				}
				if r.Header.Get(k) != missing.Header.Get(k) {
					t.Errorf("header %s differs: %q vs %q", k, r.Header.Get(k), missing.Header.Get(k))
				}
			}
		})
	}
	// A user token on the bare path is told how to address a project.
	if r := w.a.mp(w.alice, "GET", "/api/v1/messages", ""); r.StatusCode != http.StatusForbidden {
		t.Errorf("user token on the bare path: %d", r.StatusCode)
	}
}

// snapshot is the state of project two that the attacks below must not change.
type snapshot struct {
	total, unread int64
	tags          []string
	read          bool
	subject       string
}

func takeSnapshot(t *testing.T, w *world, tn tenant) snapshot {
	t.Helper()
	repo := messages.New(w.a.pool)
	scope := authz.Trusted(tn.pid)
	counts, err := repo.CountFiltered(context.Background(), scope, search.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	tags, err := repo.TagCounts(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot{total: counts.Total, unread: counts.Unread}
	for _, tc := range tags {
		s.tags = append(s.tags, tc.Name)
	}
	d, err := repo.Get(context.Background(), scope, tn.mid)
	if err != nil {
		t.Fatalf("project two lost its message: %v", err)
	}
	s.read, s.subject = d.ReadAt != nil, d.Subject
	return s
}

// TestCompatCrossTenantLeakage tries every compat route with the identifiers
// of project two while authenticated as project one, and checks that nothing
// of project two is read or changed.
func TestCompatCrossTenantLeakage(t *testing.T) {
	w := newWorld(t)
	foreign := itoa64(w.p2.mid)
	before := takeSnapshot(t, w, w.p2)
	if len(before.tags) == 0 {
		t.Fatal("the fixture should give project two a tag")
	}
	unknown := w.a.mp(w.proj1Write, "GET", "/api/v1/message/999999", "")

	t.Run("reading by id answers like an unknown id", func(t *testing.T) {
		for _, p := range []string{
			"/api/v1/message/" + foreign, "/api/v1/message/" + foreign + "/headers", "/api/v1/message/" + foreign + "/raw",
			"/api/v1/message/" + foreign + "/part/0", "/api/v1/message/" + foreign + "/part/1",
			"/view/" + foreign + ".html", "/view/" + foreign + ".txt",
		} {
			r := w.a.mp(w.proj1Write, "GET", p, "")
			if r.StatusCode != unknown.StatusCode || r.text() != unknown.text() {
				t.Errorf("%s: %d %q", p, r.StatusCode, r.body)
			}
		}
	})

	t.Run("lists and searches only show the own project", func(t *testing.T) {
		for _, p := range []string{
			"/api/v1/messages?limit=0", "/api/v1/search?query=" + url.QueryEscape("from:reports"),
			"/api/v1/search?query=" + url.QueryEscape("tag:t") + "&limit=0", "/api/v1/tags", "/api/v1/info",
			"/api/v1/message/latest",
		} {
			r := w.a.mp(w.proj1Write, "GET", p, "")
			if strings.Contains(r.text(), `"ID":"`+foreign+`"`) || strings.Contains(r.text(), `"id":"`+foreign+`"`) {
				t.Errorf("%s leaks the foreign message: %s", p, r.body)
			}
		}
		var l listBody
		w.a.mustMP(w.proj1Write, "GET", "/api/v1/messages", "", 200).decode(t, &l)
		if l.Total != 1 {
			t.Errorf("total counts another project: %d", l.Total)
		}
	})

	t.Run("writing by id has no effect and looks like unknown ids", func(t *testing.T) {
		unknownWrite := w.a.mp(w.proj1Write, "PUT", "/api/v1/messages", `{"IDs":["999999"],"Read":true}`)
		attacks := []struct{ method, path, body string }{
			{"PUT", "/api/v1/messages", `{"IDs":["` + foreign + `"],"Read":true}`},
			{"PUT", "/api/v1/messages", `{"IDs":["` + foreign + `","999999"],"Read":true}`},
			{"DELETE", "/api/v1/messages", `{"IDs":["` + foreign + `"]}`},
			{"PUT", "/api/v1/tags", `{"IDs":["` + foreign + `"],"Tags":["pwned"]}`},
			{"PUT", "/api/v1/tags", `{"IDs":["` + foreign + `"],"Tags":[]}`},
		}
		for _, a := range attacks {
			r := w.a.mp(w.proj1Write, a.method, a.path, a.body)
			if r.StatusCode != http.StatusOK || r.text() != "ok" {
				t.Errorf("%s %s: %d %s (unknown ids give %d %s)", a.method, a.path, r.StatusCode, r.body, unknownWrite.StatusCode, unknownWrite.body)
			}
		}
		if got := takeSnapshot(t, w, w.p2); got.total != before.total || got.read != before.read ||
			strings.Join(got.tags, ",") != strings.Join(before.tags, ",") {
			t.Errorf("project two changed: before %+v after %+v", before, got)
		}
	})

	t.Run("tags of another project can not be renamed or deleted", func(t *testing.T) {
		repo := messages.New(w.a.pool)
		if err := repo.ReplaceTags(context.Background(), authz.Trusted(w.p2.pid), []int64{w.p2.mid}, []string{"only-two"}); err != nil {
			t.Fatal(err)
		}
		for _, a := range []struct{ method, path, body string }{
			{"PUT", "/api/v1/tags/only-two", `{"Name":"renamed"}`},
			{"DELETE", "/api/v1/tags/only-two", ""},
		} {
			if r := w.a.mp(w.proj1Write, a.method, a.path, a.body); r.StatusCode != http.StatusBadRequest {
				t.Errorf("%s %s: status %d: %s", a.method, a.path, r.StatusCode, r.body)
			}
		}
		tags, err := repo.TagCounts(context.Background(), authz.Trusted(w.p2.pid))
		if err != nil || len(tags) != 1 || tags[0].Name != "only-two" {
			t.Errorf("project two tags: %v %v", tags, err)
		}
		before.tags = []string{"only-two"}
	})

	t.Run("bulk operations stay inside the project", func(t *testing.T) {
		w.a.mustMP(w.proj1Write, "PUT", "/api/v1/messages", `{"Read":true}`, 200)
		w.a.mustMP(w.proj1Write, "PUT", "/api/v1/messages", `{"Search":"from:reports","Read":true}`, 200)
		w.a.mustMP(w.proj1Write, "DELETE", "/api/v1/search?query="+url.QueryEscape("from:reports"), "", 200)
		w.a.mustMP(w.proj1Write, "DELETE", "/api/v1/messages", "", 200)
		got := takeSnapshot(t, w, w.p2)
		if got.total != before.total || got.unread != before.unread || got.read != before.read {
			t.Errorf("project two changed: before %+v after %+v", before, got)
		}
		if got.total != 1 || got.unread != 1 {
			t.Errorf("project two should still hold its unread message: %+v", got)
		}
		// With project one empty, "latest" must not fall through to project two.
		if r := w.a.mp(w.proj1Write, "GET", "/api/v1/message/latest", ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("latest of an empty project: %d %s", r.StatusCode, r.body)
		}
	})
}
