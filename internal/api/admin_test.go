package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/testutil"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const v1 = api.V1Prefix

func TestUserManagement(t *testing.T) {
	a := newApp(t)
	var u users.User
	a.mustJSON(http.MethodPost, v1+"/users", map[string]any{"email": "dev@example.test", "name": "Dev", "password": userPassword}, http.StatusCreated, &u)
	if u.ID == 0 || u.IsAdmin || !u.MustChangePassword || u.Email != "dev@example.test" {
		t.Errorf("created: %+v", u)
	}
	resp, body := a.do(http.MethodGet, v1+"/users/"+itoa64(u.ID), nil)
	status(t, resp, body, http.StatusOK)
	if strings.Contains(string(body), "argon2") || strings.Contains(string(body), "password") && !strings.Contains(string(body), "must_change_password") {
		t.Errorf("user JSON leaks credentials: %s", body)
	}

	// Validation and conflicts.
	for name, in := range map[string]map[string]any{
		"short password": {"email": "a@example.test", "password": "short"},
		"bad email":      {"email": "not-an-email", "password": userPassword},
		"display name":   {"email": "Bob <b@example.test>", "password": userPassword},
		"unknown field":  {"email": "c@example.test", "password": userPassword, "role": "root"},
	} {
		resp, body := a.do(http.MethodPost, v1+"/users", in)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	a.mustJSON(http.MethodPost, v1+"/users", map[string]any{"email": "DEV@example.test", "password": userPassword}, http.StatusConflict, nil)

	var list struct{ Items []users.User }
	a.mustJSON(http.MethodGet, v1+"/users", nil, http.StatusOK, &list)
	if len(list.Items) != 2 {
		t.Errorf("list: %+v", list)
	}
	a.mustJSON(http.MethodGet, v1+"/users/999999", nil, http.StatusNotFound, nil)

	a.mustJSON(http.MethodPatch, v1+"/users/"+itoa64(u.ID), map[string]any{"name": "Developer", "is_admin": true}, http.StatusOK, &u)
	if u.Name != "Developer" || !u.IsAdmin {
		t.Errorf("updated: %+v", u)
	}
	a.mustJSON(http.MethodPost, v1+"/users/"+itoa64(u.ID)+"/password", map[string]any{"new_password": "short"}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodPost, v1+"/users/"+itoa64(u.ID)+"/password", map[string]any{"new_password": otherPass}, http.StatusNoContent, nil)
	b := a.browser()
	b.mustLogin("dev@example.test", otherPass)
	resp, body = b.do(http.MethodGet, v1+"/projects", nil)
	status(t, resp, body, http.StatusForbidden) // the reset forces a change
}

func TestLastAdministratorIsProtected(t *testing.T) {
	a := newApp(t)
	self := itoa64(a.admin)
	a.mustJSON(http.MethodPatch, v1+"/users/"+self, map[string]any{"is_admin": false}, http.StatusConflict, nil)
	a.mustJSON(http.MethodPatch, v1+"/users/"+self, map[string]any{"disabled": true}, http.StatusConflict, nil)

	second := testutil.User(t, a.pool, "second@example.test", true)
	a.mustJSON(http.MethodPatch, v1+"/users/"+self, map[string]any{"is_admin": false}, http.StatusOK, nil)
	// The former administrator now lacks the power, the second one has it.
	a.mustJSON(http.MethodPatch, v1+"/users/"+itoa64(second), map[string]any{"disabled": true}, http.StatusForbidden, nil)
	other := a.userToken(second, "manage")
	a.mustJSONAs(other, http.MethodPatch, v1+"/users/"+itoa64(second), map[string]any{"disabled": true}, http.StatusConflict, nil)
}

func TestGroupsAndProjectRoles(t *testing.T) {
	a := newApp(t)
	p := a.project("Shop")
	other := a.project("Other")

	var g groups.Group
	a.mustJSON(http.MethodPost, v1+"/groups", map[string]any{"name": "Devs"}, http.StatusCreated, &g)
	a.mustJSON(http.MethodPost, v1+"/groups", map[string]any{"name": "devs"}, http.StatusConflict, nil)
	a.mustJSON(http.MethodPost, v1+"/groups", map[string]any{"name": ""}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodPatch, v1+"/groups/"+itoa64(g.ID), map[string]any{"name": "Developers"}, http.StatusOK, &g)
	if g.Name != "Developers" {
		t.Errorf("rename: %+v", g)
	}

	uid := testutil.User(t, a.pool, "member@example.test", false)
	tok := a.userToken(uid, "manage")
	a.mustJSON(http.MethodPut, v1+"/groups/"+itoa64(g.ID)+"/members/"+itoa64(uid), map[string]any{}, http.StatusNoContent, nil)
	a.mustJSON(http.MethodPut, v1+"/groups/"+itoa64(g.ID)+"/members/"+itoa64(uid), map[string]any{}, http.StatusNoContent, nil) // idempotent
	a.mustJSON(http.MethodPut, v1+"/groups/"+itoa64(g.ID)+"/members/999999", map[string]any{}, http.StatusNotFound, nil)
	var members struct{ Items []users.User }
	a.mustJSON(http.MethodGet, v1+"/groups/"+itoa64(g.ID)+"/members", nil, http.StatusOK, &members)
	if len(members.Items) != 1 || members.Items[0].ID != uid {
		t.Errorf("members: %+v", members)
	}

	// Still no role: the project is invisible.
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, ""), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodPut, pp(p.ID, "/groups/%d", g.ID), map[string]any{"role": "root"}, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodPut, pp(p.ID, "/groups/%d", g.ID), map[string]any{"role": "viewer"}, http.StatusNoContent, nil)
	a.mustJSON(http.MethodPut, pp(p.ID, "/groups/999999"), map[string]any{"role": "viewer"}, http.StatusNotFound, nil)
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, ""), nil, http.StatusOK, nil)
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, "/credentials"), nil, http.StatusForbidden, nil)
	a.mustJSONAs(tok, http.MethodGet, pp(other.ID, ""), nil, http.StatusNotFound, nil)

	// Upgrading the role takes effect at once, a user in two groups gets the highest.
	a.mustJSON(http.MethodPut, pp(p.ID, "/groups/%d", g.ID), map[string]any{"role": "owner"}, http.StatusNoContent, nil)
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, "/credentials"), nil, http.StatusOK, nil)
	var assigned struct{ Items []groups.Assignment }
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, "/groups"), nil, http.StatusOK, &assigned)
	if len(assigned.Items) != 1 || assigned.Items[0].Role != "owner" || assigned.Items[0].GroupID != g.ID {
		t.Errorf("assignments: %+v", assigned)
	}

	// The owner can hand out roles on its own project only.
	a.mustJSONAs(tok, http.MethodPut, pp(p.ID, "/groups/%d", g.ID), map[string]any{"role": "member"}, http.StatusNoContent, nil)
	a.mustJSONAs(tok, http.MethodPut, pp(other.ID, "/groups/%d", g.ID), map[string]any{"role": "owner"}, http.StatusNotFound, nil)

	a.mustJSON(http.MethodDelete, pp(p.ID, "/groups/%d", g.ID), nil, http.StatusNoContent, nil)
	a.mustJSON(http.MethodDelete, pp(p.ID, "/groups/%d", g.ID), nil, http.StatusNotFound, nil)
	a.mustJSONAs(tok, http.MethodGet, pp(p.ID, ""), nil, http.StatusNotFound, nil)

	a.mustJSON(http.MethodDelete, v1+"/groups/"+itoa64(g.ID)+"/members/"+itoa64(uid), nil, http.StatusNoContent, nil)
	a.mustJSON(http.MethodDelete, v1+"/groups/"+itoa64(g.ID)+"/members/"+itoa64(uid), nil, http.StatusNotFound, nil)
	a.mustJSON(http.MethodDelete, v1+"/groups/"+itoa64(g.ID), nil, http.StatusNoContent, nil)
	a.mustJSON(http.MethodGet, v1+"/groups/"+itoa64(g.ID), nil, http.StatusNotFound, nil)
}

func TestAPITokens(t *testing.T) {
	a := newApp(t)
	p := a.project("P")
	uid := testutil.User(t, a.pool, "u@example.test", false)
	g := testutil.Group(t, a.pool, "g")
	testutil.Member(t, a.pool, g, uid)
	testutil.Assign(t, a.pool, p.ID, g, "owner")
	owner := a.userToken(uid, "manage")

	t.Run("user token", func(t *testing.T) {
		var issued tokens.Issued
		a.mustJSONAs(owner, http.MethodPost, v1+"/users/me/tokens", map[string]any{"label": "ci", "scopes": []string{"write", "read", "read"}}, http.StatusCreated, &issued)
		if !strings.HasPrefix(issued.Secret, "spu_") || len(issued.Secret) < 40 || issued.Prefix != issued.Secret[:12] ||
			strings.Join(issued.Scopes, ",") != "read,write" {
			t.Fatalf("issued: %+v", issued)
		}
		var stored int
		err := a.pool.QueryRow(context.Background(),
			"SELECT count(*) FROM api_tokens WHERE octet_length(token_hmac) = 32 AND prefix = $1 AND position($2 in prefix || label) = 0",
			issued.Prefix, issued.Secret).Scan(&stored)
		if err != nil || stored != 1 {
			t.Errorf("only an HMAC and the display prefix may be stored: %d %v", stored, err)
		}

		// It works, within its scope, and the listing never shows the secret.
		a.mustJSONAs(issued.Secret, http.MethodGet, pp(p.ID, "/messages"), nil, http.StatusOK, nil)
		a.mustJSONAs(issued.Secret, http.MethodPost, pp(p.ID, "/credentials"), map[string]any{}, http.StatusForbidden, nil)
		resp, body := a.doAs(owner, http.MethodGet, v1+"/users/me/tokens", nil)
		status(t, resp, body, http.StatusOK)
		if strings.Contains(string(body), issued.Secret) || strings.Contains(string(body), "secret") {
			t.Errorf("listing leaks the secret: %s", body)
		}

		// A token can not mint more power than it has.
		a.mustJSONAs(issued.Secret, http.MethodPost, v1+"/users/me/tokens", map[string]any{"scopes": []string{"manage"}}, http.StatusForbidden, nil)
		a.mustJSONAs(issued.Secret, http.MethodPost, v1+"/users/me/tokens", map[string]any{"scopes": []string{"read"}}, http.StatusCreated, nil)

		a.mustJSONAs(owner, http.MethodDelete, v1+"/users/me/tokens/"+itoa64(issued.ID), nil, http.StatusNoContent, nil)
		a.mustJSONAs(owner, http.MethodDelete, v1+"/users/me/tokens/"+itoa64(issued.ID), nil, http.StatusNotFound, nil)
		a.mustJSONAs(issued.Secret, http.MethodGet, pp(p.ID, "/messages"), nil, http.StatusUnauthorized, nil)
	})

	t.Run("validation", func(t *testing.T) {
		for name, in := range map[string]map[string]any{
			"no scopes":     {"label": "x"},
			"unknown scope": {"scopes": []string{"root"}},
			"long label":    {"label": strings.Repeat("x", 101), "scopes": []string{"read"}},
		} {
			resp, body := a.doAs(owner, http.MethodPost, v1+"/users/me/tokens", in)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: %d %s", name, resp.StatusCode, body)
			}
		}
	})

	t.Run("project token", func(t *testing.T) {
		var issued tokens.Issued
		a.mustJSONAs(owner, http.MethodPost, pp(p.ID, "/tokens"), map[string]any{"label": "ci", "scopes": []string{"read"}}, http.StatusCreated, &issued)
		if !strings.HasPrefix(issued.Secret, "spp_") || issued.ProjectID == nil || *issued.ProjectID != p.ID || issued.OwnerUserID != nil {
			t.Fatalf("issued: %+v", issued)
		}
		a.mustJSONAs(owner, http.MethodPost, pp(p.ID, "/tokens"), map[string]any{"scopes": []string{"manage"}}, http.StatusBadRequest, nil)

		a.mustJSONAs(issued.Secret, http.MethodGet, pp(p.ID, "/messages"), nil, http.StatusOK, nil)
		other := a.project("Q")
		a.mustJSONAs(issued.Secret, http.MethodGet, pp(other.ID, "/messages"), nil, http.StatusNotFound, nil)
		a.mustJSONAs(issued.Secret, http.MethodGet, pp(p.ID, "/tokens"), nil, http.StatusForbidden, nil)
		a.mustJSONAs(issued.Secret, http.MethodGet, v1+"/users/me/tokens", nil, http.StatusForbidden, nil)

		var list struct{ Items []tokens.Token }
		a.mustJSONAs(owner, http.MethodGet, pp(p.ID, "/tokens"), nil, http.StatusOK, &list)
		if len(list.Items) != 1 || list.Items[0].ID != issued.ID {
			t.Errorf("list: %+v", list)
		}
		a.mustJSONAs(owner, http.MethodDelete, pp(p.ID, "/tokens/%d", issued.ID), nil, http.StatusNoContent, nil)
		a.mustJSONAs(issued.Secret, http.MethodGet, pp(p.ID, "/messages"), nil, http.StatusUnauthorized, nil)
	})

	t.Run("garbage and lookalikes", func(t *testing.T) {
		for _, bad := range []string{"spu_", "spp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sps_whatever", "Bearer", strings.Repeat("a", 5000)} {
			a.mustJSONAs(bad, http.MethodGet, v1+"/projects", nil, http.StatusUnauthorized, nil)
		}
	})

	t.Run("administrator manages tokens of a user", func(t *testing.T) {
		var issued tokens.Issued
		a.mustJSONAs(owner, http.MethodPost, v1+"/users/me/tokens", map[string]any{"scopes": []string{"read"}}, http.StatusCreated, &issued)
		a.mustJSON(http.MethodDelete, v1+"/users/"+itoa64(uid)+"/tokens/"+itoa64(issued.ID), nil, http.StatusNoContent, nil)
		a.mustJSONAs(issued.Secret, http.MethodGet, v1+"/projects", nil, http.StatusUnauthorized, nil)
	})
}

func TestAuditLog(t *testing.T) {
	a := newApp(t)
	p := a.project("Audited")
	q := a.project("Quiet")
	uid := testutil.User(t, a.pool, "own@example.test", false)
	g := testutil.Group(t, a.pool, "g")
	testutil.Member(t, a.pool, g, uid)
	testutil.Assign(t, a.pool, p.ID, g, "owner")
	viewerID := testutil.User(t, a.pool, "view@example.test", false)
	vg := testutil.Group(t, a.pool, "vg")
	testutil.Member(t, a.pool, vg, viewerID)
	testutil.Assign(t, a.pool, p.ID, vg, "viewer")
	owner, viewer := a.userToken(uid, "manage"), a.userToken(viewerID, "manage")

	var cred struct {
		ID     int64
		Secret string
	}
	a.mustJSONAs(owner, http.MethodPost, pp(p.ID, "/credentials"), map[string]any{"label": "ci"}, http.StatusCreated, &cred)
	a.mustJSONAs(owner, http.MethodDelete, pp(p.ID, "/credentials/%d", cred.ID), nil, http.StatusNoContent, nil)
	var proj tokens.Issued
	a.mustJSONAs(owner, http.MethodPost, pp(p.ID, "/tokens"), map[string]any{"scopes": []string{"read"}}, http.StatusCreated, &proj)
	a.mustJSON(http.MethodPost, v1+"/groups", map[string]any{"name": "ops"}, http.StatusCreated, nil)
	a.mustJSON(http.MethodPost, pp(q.ID, "/credentials"), map[string]any{"label": "other"}, http.StatusCreated, nil)

	var all audit.Page
	a.mustJSON(http.MethodGet, v1+"/audit", nil, http.StatusOK, &all)
	actions := map[string]int{}
	for _, e := range all.Items {
		actions[e.Action]++
	}
	for _, want := range []string{"project.create", "credential.create", "credential.revoke", "token.create", "group.create"} {
		if actions[want] == 0 {
			t.Errorf("audit has no %q entry: %v", want, actions)
		}
	}
	for _, e := range all.Items {
		if e.Action == "credential.create" && e.ProjectID != nil && *e.ProjectID == p.ID && e.ActorEmail != "own@example.test" {
			t.Errorf("actor of a credential creation: %q", e.ActorEmail)
		}
	}
	if _, data := a.do(http.MethodGet, v1+"/audit?limit=200", nil); strings.Contains(string(data), cred.Secret) || strings.Contains(string(data), proj.Secret) {
		t.Error("the audit log must never contain secrets")
	}

	// Pagination.
	var page1, page2 audit.Page
	a.mustJSON(http.MethodGet, v1+"/audit?limit=2", nil, http.StatusOK, &page1)
	if len(page1.Items) != 2 || page1.NextCursor == "" {
		t.Fatalf("page1: %+v", page1)
	}
	a.mustJSON(http.MethodGet, v1+"/audit?limit=2&cursor="+page1.NextCursor, nil, http.StatusOK, &page2)
	if len(page2.Items) == 0 || page2.Items[0].ID >= page1.Items[1].ID {
		t.Errorf("page2: %+v", page2)
	}
	a.mustJSON(http.MethodGet, v1+"/audit?cursor=abc", nil, http.StatusBadRequest, nil)
	a.mustJSON(http.MethodGet, v1+"/audit?limit=-1", nil, http.StatusBadRequest, nil)

	// Project owners read the log of their project only.
	var mine audit.Page
	a.mustJSONAs(owner, http.MethodGet, pp(p.ID, "/audit"), nil, http.StatusOK, &mine)
	if len(mine.Items) == 0 {
		t.Fatal("no project audit entries")
	}
	for _, e := range mine.Items {
		if e.ProjectID == nil || *e.ProjectID != p.ID {
			t.Errorf("foreign entry in the project log: %+v", e)
		}
	}
	a.mustJSONAs(viewer, http.MethodGet, pp(p.ID, "/audit"), nil, http.StatusForbidden, nil)
	a.mustJSONAs(owner, http.MethodGet, pp(q.ID, "/audit"), nil, http.StatusNotFound, nil)
	a.mustJSONAs(owner, http.MethodGet, v1+"/audit", nil, http.StatusForbidden, nil)
}
