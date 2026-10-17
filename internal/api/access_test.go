package api_test

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// tenant holds the ids of everything that lives inside one project.
type tenant struct {
	pid, mid, cid, rid, tid int64
}

// world is two projects with differently privileged users, built once per test.
// alice, vera and owner1 have a role on p1 only, bob only on p2.
type world struct {
	a      *app
	p1, p2 tenant
	gid    int64 // a real group, shared
	uid    int64 // a real user, shared

	vera, alice, owner1, bob, nobody string // user tokens (scope manage, so the user's own power applies)
	ownerRead                        string // owner1 limited to scope read
	adminRead                        string // the administrator limited to scope read
	proj1Write, proj1Read            string // project tokens of p1
}

func newWorld(t *testing.T) *world {
	t.Helper()
	a := newApp(t)
	w := &world{a: a}
	w.p1, w.p2 = w.tenant("one"), w.tenant("two")

	g1 := testutil.Group(t, a.pool, "g-p1-viewer")
	g2 := testutil.Group(t, a.pool, "g-p1-member")
	g3 := testutil.Group(t, a.pool, "g-p1-owner")
	g4 := testutil.Group(t, a.pool, "g-p2-owner")
	w.gid = g1
	testutil.Assign(t, a.pool, w.p1.pid, g1, "viewer")
	testutil.Assign(t, a.pool, w.p1.pid, g2, "member")
	testutil.Assign(t, a.pool, w.p1.pid, g3, "owner")
	testutil.Assign(t, a.pool, w.p2.pid, g4, "owner")

	user := func(email string, group int64) (int64, string) {
		id := testutil.User(t, a.pool, email, false)
		if group != 0 {
			testutil.Member(t, a.pool, group, id)
		}
		return id, a.userToken(id, "manage")
	}
	var owner1ID int64
	_, w.vera = user("vera@example.test", g1)
	_, w.alice = user("alice@example.test", g2)
	owner1ID, w.owner1 = user("owner1@example.test", g3)
	_, w.bob = user("bob@example.test", g4)
	w.uid, w.nobody = user("nobody@example.test", 0)
	w.ownerRead = a.userToken(owner1ID, "read")
	w.adminRead = a.userToken(a.admin, "read")
	w.proj1Write = a.projectToken(w.p1.pid, "write")
	w.proj1Read = a.projectToken(w.p1.pid, "read")
	return w
}

// tenant creates a project with one message, credential, tag rule and token.
func (w *world) tenant(slug string) tenant {
	t, a := w.a.t, w.a
	ctx := context.Background()
	p := a.project(slug)
	cred, err := credentials.New(a.pool, testSecretKey).Create(ctx, authz.Trusted(p.ID), "c")
	if err != nil {
		t.Fatal(err)
	}
	rule, err := projects.New(a.pool).CreateTagRule(ctx, authz.Trusted(p.ID),
		projects.TagRuleInput{Kind: "recipient", Pattern: "*", Tag: "t"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.toks.CreateForProject(ctx, authz.Trusted(p.ID), "t", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	return tenant{pid: p.ID, mid: a.capture(p.ID, "attachments_inline.eml"), cid: cred.ID, rid: rule.ID, tid: tok.ID}
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// expand fills the {params} of a route path. A route that uses a parameter
// without a fixture fails the test, so new endpoints can not slip past it.
func (w *world) expand(t *testing.T, path string, ids map[string]int64) string {
	t.Helper()
	return placeholder.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		if name == "ordinal" {
			return "0"
		}
		id, ok := ids[name]
		if !ok {
			t.Fatalf("route %s uses {%s} but the test has no fixture for it: add one to access_test.go", path, name)
		}
		return strconv.FormatInt(id, 10)
	})
}

func (t tenant) ids(w *world) map[string]int64 {
	return map[string]int64{
		"pid": t.pid, "mid": t.mid, "cid": t.cid, "rid": t.rid, "tid": t.tid, "gid": w.gid, "uid": w.uid,
	}
}

func isProjectRoute(r api.RouteInfo) bool { return r.Access == "project" || r.Access == "signed" }

func isStream(r api.RouteInfo) bool { return strings.HasSuffix(r.Path, "/events") }

// send performs a route request. Unsafe methods get an empty JSON object.
func (w *world) send(token string, r api.RouteInfo, path string) (*http.Response, string) {
	var body any
	if r.Method != http.MethodGet {
		body = map[string]any{}
	}
	resp, data := w.a.doAs(token, r.Method, api.V1Prefix+path, body)
	return resp, string(data)
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	w := newWorld(t)
	for _, r := range api.Routes() {
		if r.Access == "public" {
			continue
		}
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			resp, body := w.send("", r, w.expand(t, r.Path, w.p1.ids(w)))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status %d, want 401: %s", resp.StatusCode, body)
			}
		})
	}
}

// TestCrossTenantLeakage requests every project route with the identifiers of
// a project the caller has no access to, and expects 404 with a body that is
// identical to the answer for a project that does not exist.
func TestCrossTenantLeakage(t *testing.T) {
	w := newWorld(t)
	const missing = 999999
	callers := map[string]string{
		"member of p1":      w.alice,
		"owner of p1":       w.owner1,
		"viewer of p1":      w.vera,
		"user without role": w.nobody,
		"project token p1":  w.proj1Write,
	}
	for name, token := range callers {
		for _, r := range api.Routes() {
			if !isProjectRoute(r) {
				continue
			}
			t.Run(name+"/"+r.Method+" "+r.Path, func(t *testing.T) {
				foreign := w.expand(t, r.Path, w.p2.ids(w))
				resp, body := w.send(token, r, foreign)
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("foreign project: status %d, want 404: %s", resp.StatusCode, body)
				}
				ghost := w.p2.ids(w)
				ghost["pid"] = missing
				_, ghostBody := w.send(token, r, w.expand(t, r.Path, ghost))
				if body != ghostBody {
					t.Errorf("a foreign project must answer exactly like a missing one:\n foreign: %s\n missing: %s", body, ghostBody)
				}
			})
		}
	}
}

// TestChildIdsOfAnotherProject uses the caller's own project id together with
// ids of children (message, credential, rule, token) of another project.
func TestChildIdsOfAnotherProject(t *testing.T) {
	w := newWorld(t)
	for name, token := range map[string]string{"owner of p1": w.owner1, "administrator": w.a.token} {
		for _, r := range api.Routes() {
			if !isProjectRoute(r) || !strings.ContainsAny(r.Path, "{") || !hasChildParam(r.Path) {
				continue
			}
			t.Run(name+"/"+r.Method+" "+r.Path, func(t *testing.T) {
				ids := w.p2.ids(w)
				ids["pid"] = w.p1.pid
				resp, body := w.send(token, r, w.expand(t, r.Path, ids))
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("status %d, want 404: %s", resp.StatusCode, body)
				}
			})
		}
	}
	// Nothing of p2 was touched by those requests.
	var rules struct{ Items []projects.TagRule }
	w.a.mustJSON(http.MethodGet, pp(w.p2.pid, "/tag-rules"), nil, http.StatusOK, &rules)
	if len(rules.Items) != 1 {
		t.Errorf("p2 tag rules after the attempts: %+v", rules.Items)
	}
}

func hasChildParam(path string) bool {
	for _, p := range []string{"{mid}", "{cid}", "{rid}", "{tid}"} {
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

func TestListingsOnlyShowVisibleData(t *testing.T) {
	w := newWorld(t)
	var list struct{ Items []projects.Project }
	w.a.mustJSONAs(w.alice, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != w.p1.pid {
		t.Errorf("alice sees %+v, want only p1", list.Items)
	}
	w.a.mustJSONAs(w.bob, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != w.p2.pid {
		t.Errorf("bob sees %+v, want only p2", list.Items)
	}
	w.a.mustJSONAs(w.nobody, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("a user without a role sees %+v", list.Items)
	}
	w.a.mustJSONAs(w.proj1Read, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != w.p1.pid {
		t.Errorf("a project token sees %+v, want only its project", list.Items)
	}
	w.a.mustJSON(http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 2 {
		t.Errorf("the administrator sees %d projects, want 2", len(list.Items))
	}
}

// actor is a principal with its effective role on p1.
type actor struct {
	name  string
	token string
	role  authz.Role
}

func (w *world) actors() []actor {
	return []actor{
		{"viewer", w.vera, authz.RoleViewer},
		{"member", w.alice, authz.RoleMember},
		{"owner", w.owner1, authz.RoleOwner},
		{"administrator", w.a.token, authz.RoleAdmin},
		{"owner limited to read scope", w.ownerRead, authz.RoleViewer},
		{"project token write", w.proj1Write, authz.RoleMember},
		{"project token read", w.proj1Read, authz.RoleViewer},
		{"user of another project", w.bob, authz.RoleNone},
		{"user without role", w.nobody, authz.RoleNone},
	}
}

// TestAuthorizationMatrixOverHTTP checks every project route against every
// kind of principal: the answer must follow the role matrix.
func TestAuthorizationMatrixOverHTTP(t *testing.T) {
	w := newWorld(t)
	absent := w.p1.ids(w)
	for k := range absent {
		if k != "pid" {
			absent[k] = 999999 // children that do not exist: allowed callers get 404 or 400, never 403
		}
	}
	for _, act := range w.actors() {
		for _, r := range api.Routes() {
			if !isProjectRoute(r) {
				continue
			}
			t.Run(act.name+"/"+r.Method+" "+r.Path, func(t *testing.T) {
				capability := authz.Capability(r.Capability)
				switch {
				case act.role == authz.RoleNone:
					resp, body := w.send(act.token, r, w.expand(t, r.Path, absent))
					expectStatus(t, resp, body, http.StatusNotFound)
				case !act.role.Allows(capability):
					resp, body := w.send(act.token, r, w.expand(t, r.Path, absent))
					expectStatus(t, resp, body, http.StatusForbidden)
				case isStream(r):
					// Allowed callers get an endless stream, covered by TestEventStream.
				default:
					resp, body := w.send(act.token, r, w.expand(t, r.Path, absent))
					if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
						t.Errorf("an allowed caller got %d: %s", resp.StatusCode, body)
					}
				}
			})
		}
	}
}

func TestGlobalRoutesNeedAnAdministrator(t *testing.T) {
	w := newWorld(t)
	absent := map[string]int64{"gid": 999999, "uid": 999999, "tid": 999999, "pid": w.p1.pid}
	denied := map[string]string{
		"owner":                    w.owner1,
		"user without role":        w.nobody,
		"administrator read scope": w.adminRead,
		"project token":            w.proj1Write,
	}
	for _, r := range api.Routes() {
		if r.Access != "global" {
			continue
		}
		for name, token := range denied {
			t.Run(name+"/"+r.Method+" "+r.Path, func(t *testing.T) {
				resp, body := w.send(token, r, w.expand(t, r.Path, absent))
				expectStatus(t, resp, body, http.StatusForbidden)
			})
		}
		t.Run("administrator/"+r.Method+" "+r.Path, func(t *testing.T) {
			resp, body := w.send(w.a.token, r, w.expand(t, r.Path, absent))
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("an administrator got %d: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestUserRoutesRejectProjectTokens(t *testing.T) {
	w := newWorld(t)
	absent := map[string]int64{"tid": 999999}
	for _, r := range api.Routes() {
		if r.Access != "user" {
			continue
		}
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			resp, body := w.send(w.proj1Write, r, w.expand(t, r.Path, absent))
			expectStatus(t, resp, body, http.StatusForbidden)
		})
	}
}

func expectStatus(t *testing.T, resp *http.Response, body string, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Errorf("status %d, want %d: %s", resp.StatusCode, want, body)
	}
}
