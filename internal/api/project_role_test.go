package api_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// roleView is the part of a project the role tests look at.
type roleView struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

// rolesOnP1 lists callers with the role name the API must report on p1. An empty
// string means the project must stay invisible.
func (w *world) rolesOnP1() []struct{ name, token, want string } {
	return []struct{ name, token, want string }{
		{"viewer", w.vera, "viewer"},
		{"member", w.alice, "member"},
		{"owner", w.owner1, "owner"},
		{"administrator", w.a.token, "owner"},
		{"administrator limited to read", w.adminRead, "viewer"},
		{"owner limited to read", w.ownerRead, "viewer"},
		{"project token write", w.proj1Write, "member"},
		{"project token read", w.proj1Read, "viewer"},
		{"user of another project", w.bob, ""},
		{"user without role", w.nobody, ""},
	}
}

func TestProjectRoleOnGet(t *testing.T) {
	w := newWorld(t)
	path := api.V1Prefix + "/projects/" + strconv.FormatInt(w.p1.pid, 10)
	for _, tc := range w.rolesOnP1() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == "" {
				resp, body := w.a.doAs(tc.token, http.MethodGet, path, nil)
				expectStatus(t, resp, string(body), http.StatusNotFound)
				return
			}
			var got roleView
			w.a.mustJSONAs(tc.token, http.MethodGet, path, nil, http.StatusOK, &got)
			if got.Role != tc.want {
				t.Errorf("role = %q, want %q", got.Role, tc.want)
			}
		})
	}
}

// TestProjectRoleOnList checks the list against the same table as get, and
// that a caller without a role never sees the project.
func TestProjectRoleOnList(t *testing.T) {
	w := newWorld(t)
	for _, tc := range w.rolesOnP1() {
		t.Run(tc.name, func(t *testing.T) {
			var list struct{ Items []roleView }
			w.a.mustJSONAs(tc.token, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
			var seen *roleView
			for i, it := range list.Items {
				if it.ID == w.p1.pid {
					seen = &list.Items[i]
				}
				if it.Role == "" {
					t.Errorf("project %d listed without a role", it.ID)
				}
			}
			switch {
			case tc.want == "" && seen != nil:
				t.Errorf("project is listed with role %q, want it hidden", seen.Role)
			case tc.want != "" && seen == nil:
				t.Errorf("project is not listed, want role %q", tc.want)
			case tc.want != "" && seen.Role != tc.want:
				t.Errorf("role = %q, want %q", seen.Role, tc.want)
			}
		})
	}
}

// TestProjectRoleListMatchesGet keeps the two code paths honest: whatever the
// list says about a project, get says too.
func TestProjectRoleListMatchesGet(t *testing.T) {
	w := newWorld(t)
	for _, tc := range w.rolesOnP1() {
		t.Run(tc.name, func(t *testing.T) {
			var list struct{ Items []roleView }
			w.a.mustJSONAs(tc.token, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
			for _, it := range list.Items {
				var got roleView
				w.a.mustJSONAs(tc.token, http.MethodGet, api.V1Prefix+"/projects/"+strconv.FormatInt(it.ID, 10), nil, http.StatusOK, &got)
				if got.Role != it.Role {
					t.Errorf("project %d: list says %q, get says %q", it.ID, it.Role, got.Role)
				}
			}
		})
	}
}

// TestProjectRoleTakesTheHighestGroupRole covers a user in several groups of
// one project: one row, the highest role.
func TestProjectRoleTakesTheHighestGroupRole(t *testing.T) {
	w := newWorld(t)
	g := testutil.Group(t, w.a.pool, "g-p1-owner-too")
	testutil.Assign(t, w.a.pool, w.p1.pid, g, "owner")
	uid := testutil.User(t, w.a.pool, "multi@example.test", false)
	testutil.Member(t, w.a.pool, w.gid, uid) // viewer group of p1
	testutil.Member(t, w.a.pool, g, uid)
	token := w.a.userToken(uid, "manage")
	var list struct{ Items []roleView }
	w.a.mustJSONAs(token, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Role != "owner" {
		t.Errorf("items = %+v, want one project with role owner", list.Items)
	}
}

func TestCreatedProjectCarriesTheCreatorRole(t *testing.T) {
	w := newWorld(t)
	var got roleView
	w.a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": "fresh"}, http.StatusCreated, &got)
	if got.Role != "owner" {
		t.Errorf("role = %q, want owner", got.Role)
	}
}
