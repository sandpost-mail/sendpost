package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func userID(t *testing.T, w *world, email string) int64 {
	t.Helper()
	var id int64
	if err := w.a.pool.QueryRow(t.Context(), "SELECT id FROM users WHERE email = $1", email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func forcePasswordChange(t *testing.T, w *world, uid int64, on bool) {
	t.Helper()
	if _, err := w.a.pool.Exec(t.Context(), "UPDATE users SET must_change_password = $1 WHERE id = $2", on, uid); err != nil {
		t.Fatal(err)
	}
}

// A forced password change (the recovery step after an admin reset) must
// also stop the user's existing API tokens, on both API surfaces.
func TestForcedPasswordChangeStopsUserTokens(t *testing.T) {
	w := newWorld(t)
	alice := userID(t, w, "alice@example.test")
	forcePasswordChange(t, w, alice, true)

	native := []struct{ method, path string }{
		{http.MethodGet, "/projects"},
		{http.MethodGet, pp(w.p1.pid, "/messages")[len(api.V1Prefix):]},
		{http.MethodGet, pp(w.p1.pid, "/wait?timeout=0")[len(api.V1Prefix):]},
		{http.MethodGet, "/users/me/tokens"},
	}
	for _, rt := range native {
		resp, body := w.a.doAs(w.alice, rt.method, api.V1Prefix+rt.path, nil)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "password_change_required") {
			t.Errorf("native %s %s: %d %s", rt.method, rt.path, resp.StatusCode, body)
		}
	}
	for _, style := range []authStyle{authBasic, authBearer} {
		r := w.a.compat(style, w.alice, http.MethodGet, "/p/one/api/v1/messages", "")
		if r.StatusCode != http.StatusForbidden || r.text() != "password change required" {
			t.Errorf("compat style %d: %d %s", style, r.StatusCode, r.body)
		}
	}
	// The flow to change the password stays open.
	if resp, body := w.a.doAs(w.alice, http.MethodGet, api.V1Prefix+"/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("me: %d %s", resp.StatusCode, body)
	}

	forcePasswordChange(t, w, alice, false)
	w.a.mustJSONAs(w.alice, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, nil)
	w.a.mustMP(w.alice, http.MethodGet, "/p/one/api/v1/messages", "", http.StatusOK)
}

// A release through the compat API with a user token is attributed to that
// user in the audit log, like the native release.
func TestCompatReleaseRecordsTheActor(t *testing.T) {
	w := newWorld(t)
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	w.deliverySetup(relay, true, "allow", "@corp.example")
	path := "/p/one/api/v1/message/" + itoa64(w.p1.mid) + "/release"
	w.a.mustMP(w.alice, http.MethodPost, path, `{"To":["a@corp.example"]}`, http.StatusOK)

	var actor *int64
	err := w.a.pool.QueryRow(t.Context(),
		"SELECT actor_user_id FROM audit_log WHERE action = 'message.release' ORDER BY id DESC LIMIT 1").Scan(&actor)
	if err != nil {
		t.Fatal(err)
	}
	if want := userID(t, w, "alice@example.test"); actor == nil || *actor != want {
		t.Errorf("actor %v, want %d", actor, want)
	}
}

// Compat writes with a body must be declared JSON, so a cross site form post
// riding on cached Basic credentials can not reach them.
func TestCompatWritesNeedJSONBodies(t *testing.T) {
	w := newWorld(t)
	send := func(method, path, ct, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, w.a.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("x", w.proj1Write)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	release := "/api/v1/message/" + itoa64(w.p1.mid) + "/release"
	cases := []struct {
		name, method, path, ct, body string
		want                         int
	}{
		{"form post release", http.MethodPost, release, "text/plain", `{"To":["a@corp.example"],"x":"="}`, http.StatusUnsupportedMediaType},
		{"no type release", http.MethodPost, release, "", `{"To":["a@corp.example"]}`, http.StatusUnsupportedMediaType},
		{"urlencoded set read", http.MethodPut, "/api/v1/messages", "application/x-www-form-urlencoded", `{"Read":true}`, http.StatusUnsupportedMediaType},
		{"json set read", http.MethodPut, "/api/v1/messages", "application/json; charset=utf-8", `{"Read":true}`, http.StatusOK},
		{"bodyless delete needs no type", http.MethodDelete, "/api/v1/messages", "", "", http.StatusOK},
		{"too large", http.MethodPut, "/api/v1/messages", "application/json", `{"IDs":["` + strings.Repeat("1", 2<<20) + `"]}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if resp := send(tc.method, tc.path, tc.ct, tc.body); resp.StatusCode != tc.want {
				t.Errorf("status %d want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// openStream starts an SSE request and returns its body once "ready" was read.
func openStream(t *testing.T, a *app, token, path string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, a.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	rd := bufio.NewReader(resp.Body)
	if err := readUntil(rd, "event: ready"); err != nil {
		t.Fatalf("no ready event: %v", err)
	}
	return resp, rd
}

func readUntil(rd *bufio.Reader, prefix string) error {
	for {
		line, err := rd.ReadString('\n')
		if strings.HasPrefix(line, prefix) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read stream: %w", err)
		}
	}
}

// An open event stream re-checks access, so revoking the token ends it.
func TestEventStreamEndsWhenAccessIsRevoked(t *testing.T) {
	api.SetStreamRecheck(t, 50*time.Millisecond)
	w := newWorld(t)
	alice := userID(t, w, "alice@example.test")
	issued, err := w.a.toks.CreateForUser(t.Context(), alice, "stream", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	_, rd := openStream(t, w.a, issued.Secret, pp(w.p1.pid, "/events"))
	w.a.mustJSONAs(w.alice, http.MethodDelete, api.V1Prefix+"/users/me/tokens/"+itoa64(issued.ID), nil, http.StatusNoContent, nil)

	ended := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, rd); ended <- err }()
	select {
	case err := <-ended:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Logf("stream ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the revoked token")
	}
}

// Removing the user from the project's group also ends the stream.
func TestEventStreamEndsWhenTheRoleIsRemoved(t *testing.T) {
	api.SetStreamRecheck(t, 50*time.Millisecond)
	w := newWorld(t)
	_, rd := openStream(t, w.a, w.alice, pp(w.p1.pid, "/events"))
	if _, err := w.a.pool.Exec(t.Context(), "DELETE FROM group_members WHERE user_id = $1", userID(t, w, "alice@example.test")); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, rd); close(ended) }()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the role")
	}
}

func TestLongLivedRequestsAreCappedPerPrincipal(t *testing.T) {
	w := newWorld(t)
	path := pp(w.p1.pid, "/events")
	for range api.MaxStreamsPerPrincipal {
		if resp, _ := openStream(t, w.a, w.alice, path); resp.StatusCode != http.StatusOK {
			t.Fatalf("stream refused early: %d", resp.StatusCode)
		}
	}
	if resp, body := w.a.doAs(w.alice, http.MethodGet, path, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream over the cap: %d %s", resp.StatusCode, body)
	}
	if resp, body := w.a.doAs(w.alice, http.MethodGet, pp(w.p1.pid, "/wait?timeout=1"), nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("wait over the cap: %d %s", resp.StatusCode, body)
	}
	// Another credential is not affected.
	if resp, _ := openStream(t, w.a, w.vera, path); resp.StatusCode != http.StatusOK {
		t.Fatalf("other principal: %d", resp.StatusCode)
	}
}

func TestWaitOrder(t *testing.T) {
	a := newApp(t)
	p := a.project("one")
	var sent []int64
	for i := range 3 {
		sent = append(sent, a.send(p.ID, "run-o", fmt.Sprintf("m%d", i), "x"))
	}
	var asc, desc waitResult
	a.mustJSON(http.MethodGet, pp(p.ID, "/wait?timeout=0&run=run-o"), nil, http.StatusOK, &asc)
	a.mustJSON(http.MethodGet, pp(p.ID, "/wait?timeout=0&run=run-o&order=desc"), nil, http.StatusOK, &desc)
	if got := waitIDs(asc); fmt.Sprint(got) != fmt.Sprint(sent) {
		t.Errorf("asc %v want %v", got, sent)
	}
	if got := waitIDs(desc); len(got) != 3 || got[0] != sent[2] || got[2] != sent[0] {
		t.Errorf("desc %v (sent %v)", got, sent)
	}
	if resp, body := a.do(http.MethodGet, pp(p.ID, "/wait?timeout=0&order=newest"), nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad order: %d %s", resp.StatusCode, body)
	}
}

func TestNativeJSONBodiesAreStrict(t *testing.T) {
	a := newApp(t)
	post := func(body string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.srv.URL+api.V1Prefix+"/projects", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(`{"name":"a"}{"name":"b"}`); got != http.StatusBadRequest {
		t.Errorf("two values: %d", got)
	}
	big, _ := json.Marshal(map[string]string{"name": strings.Repeat("x", 70<<10)})
	if got := post(string(big)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", got)
	}
	if got := post(`{"name":"ok"}`); got != http.StatusCreated {
		t.Errorf("valid: %d", got)
	}
}

// tryLogin is a login that reports errors instead of failing the test, for
// use from goroutines.
func tryLogin(ctx context.Context, a *app, email, password string) (int, error) {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return 0, fmt.Errorf("encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.srv.URL+api.V1Prefix+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("login: %w", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
