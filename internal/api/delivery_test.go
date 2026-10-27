package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const relayPassword = "relay-secret-pw-123" //nolint:gosec // test value

// deliverySetup gives p1 a transport pointing at a fake relay and a policy,
// both through the API as the owner of p1.
func (w *world) deliverySetup(relay *testutil.FakeRelay, enable bool, mode string, allow ...string) {
	w.a.t.Helper()
	body := map[string]any{
		"kind": "smtp", "host": relay.Host, "port": relay.Port, "tls_mode": "starttls",
		"username": "relay-user", "secret": relayPassword,
	}
	w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/transport"), body, http.StatusOK, nil)
	if enable {
		body["secret"], body["enabled"] = nil, true
		w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/transport"), body, http.StatusOK, nil)
	}
	w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/policy"),
		map[string]any{"mode": mode, "allowlist": allow, "redirect_address": "pm@corp.example"}, http.StatusOK, nil)
}

func releaseJobs(t *testing.T, a *app) int {
	t.Helper()
	var n int
	if err := a.pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'run' LIKE 'release:%'", delivery.DeliverKind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTransportAndPolicyEndpoints(t *testing.T) {
	w := newWorld(t)
	relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "relay-user", Pass: relayPassword})
	_ = relay
	path := pp(w.p1.pid, "/transport")

	var got struct {
		Transport *delivery.Transport `json:"transport"`
	}
	w.a.mustJSONAs(w.owner1, http.MethodGet, path, nil, http.StatusOK, &got)
	if got.Transport != nil {
		t.Fatalf("no transport yet: %+v", got.Transport)
	}

	body := map[string]any{"kind": "smtp", "host": "127.0.0.1", "port": relay.Port, "tls_mode": "starttls", "username": "relay-user", "secret": relayPassword, "enabled": true}
	resp, data := w.a.doAs(w.owner1, http.MethodPut, path, body)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(data), relayPassword) || !strings.Contains(string(data), `"has_secret":true`) || !strings.Contains(string(data), `"enabled":false`) {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	// Every read afterwards is free of the password too.
	for _, r := range []struct{ token, method, path string }{
		{w.owner1, http.MethodGet, path},
		{w.owner1, http.MethodGet, pp(w.p1.pid, "/audit")},
		{w.a.token, http.MethodGet, v1 + "/audit"},
	} {
		_, data := w.a.doAs(r.token, r.method, r.path, nil)
		if strings.Contains(string(data), relayPassword) {
			t.Errorf("%s %s leaks the relay password: %s", r.method, r.path, data)
		}
	}

	// Test endpoint: the stored settings against the fake relay (no CA trust in the API process: TLS verify fails).
	var check delivery.CheckResult
	w.a.mustJSONAs(w.owner1, http.MethodPost, path+"/test", nil, http.StatusOK, &check)
	if check.OK || check.Error != "tls failed: certificate verification failed" {
		t.Errorf("check %+v", check)
	}

	// Validation errors are 400 with a message.
	for _, bad := range []map[string]any{
		{"kind": "smtp", "host": "", "port": 25, "tls_mode": "none"},
		{"kind": "smtp", "host": "h.example", "port": 0, "tls_mode": "none"},
		{"kind": "smtp", "host": "h.example", "port": 25, "tls_mode": "none", "username": "u", "secret": "p"},
		{"kind": "smtp", "host": "h.example", "port": 25, "tls_mode": "none", "unknown_field": 1},
	} {
		w.a.mustJSONAs(w.owner1, http.MethodPut, path, bad, http.StatusBadRequest, nil)
	}
	w.a.mustJSONAs(w.owner1, http.MethodDelete, path, nil, http.StatusNoContent, nil)
	w.a.mustJSONAs(w.owner1, http.MethodDelete, path, nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.owner1, http.MethodPost, path+"/test", nil, http.StatusNotFound, nil)

	// Policy.
	var pol delivery.Policy
	w.a.mustJSONAs(w.owner1, http.MethodGet, pp(w.p1.pid, "/policy"), nil, http.StatusOK, &pol)
	if pol.Mode != "capture" || pol.Configured {
		t.Errorf("default %+v", pol)
	}
	w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/policy"),
		map[string]any{"mode": "redirect", "allowlist": []string{"Dev@Corp.example"}, "redirect_address": "pm@corp.example", "subject_prefix": "[qa]"}, http.StatusOK, &pol)
	if pol.Mode != "redirect" || pol.Allowlist[0] != "dev@corp.example" || !pol.Configured {
		t.Errorf("saved %+v", pol)
	}
	for _, bad := range []map[string]any{
		{"mode": "relay"}, {"mode": "allow", "allowlist": []string{"*"}}, {"mode": "redirect"},
	} {
		w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/policy"), bad, http.StatusBadRequest, nil)
	}
}

func TestOnlyOwnersManageDelivery(t *testing.T) {
	w := newWorld(t)
	for name, token := range map[string]string{"viewer": w.vera, "member": w.alice, "project token write": w.proj1Write, "owner read scope": w.ownerRead} {
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, "/transport"}, {http.MethodPut, "/transport"}, {http.MethodDelete, "/transport"},
			{http.MethodPost, "/transport/test"}, {http.MethodGet, "/policy"}, {http.MethodPut, "/policy"},
		} {
			resp, body := w.a.doAs(token, r.method, pp(w.p1.pid, "%s", r.path), map[string]any{})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s %s: %d %s", name, r.method, r.path, resp.StatusCode, body)
			}
		}
	}
}

func TestReleaseEndpoint(t *testing.T) {
	w := newWorld(t)
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	url := func(mid int64) string { return pp(w.p1.pid, "/messages/%d/release", mid) }

	// Nothing is configured: refused, nothing queued.
	var apiErr struct{ Error, Code string }
	w.a.mustJSONAs(w.alice, http.MethodPost, url(w.p1.mid), map[string]any{"to": []string{"a@corp.example"}}, http.StatusBadRequest, &apiErr)
	if apiErr.Code != "delivery_blocked" || apiErr.Error == "" {
		t.Errorf("error %+v", apiErr)
	}

	w.deliverySetup(relay, true, "allow", "@corp.example")
	// Off the allowlist: refused, and the message names the offender but nothing is queued.
	w.a.mustJSONAs(w.alice, http.MethodPost, url(w.p1.mid), map[string]any{"to": []string{"ok@corp.example", "evil@evil.test"}}, http.StatusBadRequest, &apiErr)
	if apiErr.Code != "delivery_blocked" || !strings.Contains(apiErr.Error, "evil@evil.test") {
		t.Errorf("error %+v", apiErr)
	}
	for _, bad := range []any{map[string]any{"to": []string{}}, map[string]any{}, map[string]any{"to": []string{"nope"}}} {
		w.a.mustJSONAs(w.alice, http.MethodPost, url(w.p1.mid), bad, http.StatusBadRequest, nil)
	}
	w.a.mustJSONAs(w.alice, http.MethodPost, url(w.p1.mid), map[string]any{"to": []string{"a@corp.example"}, "extra": 1}, http.StatusBadRequest, nil)
	if n := releaseJobs(t, w.a); n != 0 {
		t.Fatalf("%d release job(s) after refused releases", n)
	}

	// Allowed: a member may release, a viewer may not.
	w.a.mustJSONAs(w.vera, http.MethodPost, url(w.p1.mid), map[string]any{"to": []string{"a@corp.example"}}, http.StatusForbidden, nil)
	var out struct{ Items []delivery.Delivery }
	w.a.mustJSONAs(w.alice, http.MethodPost, url(w.p1.mid), map[string]any{"to": []string{"a@corp.example", "b@corp.example"}}, http.StatusAccepted, &out)
	if len(out.Items) != 2 || out.Items[0].Status != "pending" || out.Items[0].MessageID != w.p1.mid {
		t.Errorf("items %+v", out.Items)
	}
	if n := releaseJobs(t, w.a); n != 1 {
		t.Errorf("%d release job(s)", n)
	}
	if relay.Connections() != 0 {
		t.Error("the API request itself must not contact the relay")
	}

	// A viewer reads the status.
	var list struct{ Items []delivery.Delivery }
	w.a.mustJSONAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/deliveries", w.p1.mid), nil, http.StatusOK, &list)
	if len(list.Items) != 2 {
		t.Errorf("list %+v", list.Items)
	}

	// A message of p2 with p1's project id is a 404, for both routes, and nothing is queued for it.
	w.a.mustJSONAs(w.owner1, http.MethodPost, url(w.p2.mid), map[string]any{"to": []string{"a@corp.example"}}, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.owner1, http.MethodGet, pp(w.p1.pid, "/messages/%d/deliveries", w.p2.mid), nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.owner1, http.MethodPost, url(999999), map[string]any{"to": []string{"a@corp.example"}}, http.StatusNotFound, nil)
	if n := releaseJobs(t, w.a); n != 1 {
		t.Errorf("a foreign release queued a job: %d", n)
	}
}

// Owners of p1 can not read or change what p2's owner configured.
func TestDeliverySettingsDoNotCrossProjects(t *testing.T) {
	w := newWorld(t)
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	// p2 is configured by the administrator.
	w.a.mustJSON(http.MethodPut, pp(w.p2.pid, "/transport"), map[string]any{"kind": "smtp", "host": relay.Host, "port": relay.Port, "tls_mode": "none"}, http.StatusOK, nil)
	w.a.mustJSON(http.MethodPut, pp(w.p2.pid, "/policy"), map[string]any{"mode": "allow", "allowlist": []string{"@p2.example"}}, http.StatusOK, nil)

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/transport"}, {http.MethodPut, "/transport"}, {http.MethodDelete, "/transport"},
		{http.MethodPost, "/transport/test"}, {http.MethodGet, "/policy"}, {http.MethodPut, "/policy"},
	} {
		body := map[string]any{"mode": "capture", "kind": "smtp", "host": "evil.example", "port": 25, "tls_mode": "none"}
		resp, data := w.a.doAs(w.owner1, r.method, pp(w.p2.pid, "%s", r.path), body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s on a foreign project: %d %s", r.method, r.path, resp.StatusCode, data)
		}
	}
	// Nothing changed.
	var tr struct{ Transport *delivery.Transport }
	w.a.mustJSON(http.MethodGet, pp(w.p2.pid, "/transport"), nil, http.StatusOK, &tr)
	if tr.Transport == nil || tr.Transport.Host != relay.Host {
		t.Errorf("p2 transport %+v", tr.Transport)
	}
	var pol delivery.Policy
	w.a.mustJSON(http.MethodGet, pp(w.p2.pid, "/policy"), nil, http.StatusOK, &pol)
	if pol.Mode != "allow" {
		t.Errorf("p2 policy %+v", pol)
	}
	// And p1 sees none of it.
	w.a.mustJSONAs(w.owner1, http.MethodGet, pp(w.p1.pid, "/transport"), nil, http.StatusOK, &tr)
	if tr.Transport != nil {
		t.Errorf("p1 sees a transport: %+v", tr.Transport)
	}
	w.a.mustJSONAs(w.owner1, http.MethodGet, pp(w.p1.pid, "/policy"), nil, http.StatusOK, &pol)
	if pol.Configured {
		t.Errorf("p1 sees a policy: %+v", pol)
	}
}

func TestCompatRelease(t *testing.T) {
	w := newWorld(t)
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	path := "/api/v1/message/" + itoa64(w.p1.mid) + "/release"
	body := `{"To":["a@corp.example"]}`

	// Not configured: 400 with the reason, as plain text.
	r := w.a.mustMP(w.proj1Write, http.MethodPost, path, body, http.StatusBadRequest)
	if !strings.Contains(r.text(), "delivery blocked") {
		t.Errorf("body %q", r.text())
	}

	w.deliverySetup(relay, true, "allow", "@corp.example")
	w.a.mustMP(w.proj1Write, http.MethodPost, path, body, http.StatusOK)
	if got := w.a.mp(w.proj1Write, http.MethodPost, path, body).text(); got != "ok" {
		t.Errorf("answer %q", got)
	}
	if n := releaseJobs(t, w.a); n != 2 {
		t.Errorf("%d jobs", n)
	}

	// latest, the /p/{slug} form with a user token, and Bearer auth.
	w.a.mustMP(w.proj1Write, http.MethodPost, "/api/v1/message/latest/release", body, http.StatusOK)
	w.a.mustMP(w.alice, http.MethodPost, "/p/one"+path, body, http.StatusOK)
	if r := w.a.compat(authBearer, w.proj1Write, http.MethodPost, path, body); r.StatusCode != http.StatusOK {
		t.Errorf("bearer: %d %s", r.StatusCode, r.body)
	}

	// Refusals.
	w.a.mustMP(w.proj1Write, http.MethodPost, path, `{"To":["evil@evil.test"]}`, http.StatusBadRequest)
	w.a.mustMP(w.proj1Write, http.MethodPost, path, `{"To":[]}`, http.StatusBadRequest)
	w.a.mustMP(w.proj1Write, http.MethodPost, path, ``, http.StatusBadRequest)
	w.a.mustMP(w.proj1Write, http.MethodPost, path, `not json`, http.StatusBadRequest)
	w.a.mustMP(w.proj1Read, http.MethodPost, path, body, http.StatusForbidden)
	w.a.mustMP(w.vera, http.MethodPost, "/p/one"+path, body, http.StatusForbidden)
	w.a.mustMP("spp_bogus", http.MethodPost, path, body, http.StatusUnauthorized)

	// Foreign and unknown ids are the same plain 404.
	before := releaseJobs(t, w.a)
	foreign := w.a.mustMP(w.proj1Write, http.MethodPost, "/api/v1/message/"+itoa64(w.p2.mid)+"/release", body, http.StatusNotFound)
	unknown := w.a.mustMP(w.proj1Write, http.MethodPost, "/api/v1/message/999999/release", body, http.StatusNotFound)
	notnum := w.a.mustMP(w.proj1Write, http.MethodPost, "/api/v1/message/abc/release", body, http.StatusNotFound)
	if foreign.text() != unknown.text() || unknown.text() != notnum.text() {
		t.Errorf("foreign %q, unknown %q, not a number %q", foreign.text(), unknown.text(), notnum.text())
	}
	// A token of p1 on p2's slug is the same 404 as an unknown slug.
	a := w.a.mustMP(w.alice, http.MethodPost, "/p/two/api/v1/message/"+itoa64(w.p2.mid)+"/release", body, http.StatusNotFound)
	b := w.a.mustMP(w.alice, http.MethodPost, "/p/nope/api/v1/message/"+itoa64(w.p2.mid)+"/release", body, http.StatusNotFound)
	if a.text() != b.text() {
		t.Errorf("slug probing: %q vs %q", a.text(), b.text())
	}
	if releaseJobs(t, w.a) != before {
		t.Error("a refused release queued a job")
	}
}
