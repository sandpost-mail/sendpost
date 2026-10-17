package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const (
	userPassword = "correct horse battery"
	otherPass    = "another long passphrase"
)

// browser is a cookie holding client with the CSRF token of its login.
type browser struct {
	t    *testing.T
	a    *app
	c    *http.Client
	csrf string
}

func (a *app) browser() *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		a.t.Fatal(err)
	}
	return &browser{t: a.t, a: a, c: &http.Client{Jar: jar}}
}

// do sends a request. The CSRF header is added to unsafe methods unless csrf
// is overridden through withCSRF.
func (b *browser) do(method, path string, body any, headers ...string) (*http.Response, []byte) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, b.a.srv.URL+path, rdr)
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		if b.csrf != "" {
			req.Header.Set("X-CSRF-Token", b.csrf)
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (b *browser) login(email, password string) (*http.Response, []byte) {
	b.t.Helper()
	resp, data := b.do(http.MethodPost, api.V1Prefix+"/auth/login", map[string]string{"email": email, "password": password})
	if resp.StatusCode == http.StatusOK {
		var out struct {
			CSRFToken string `json:"csrf_token"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			b.t.Fatal(err)
		}
		b.csrf = out.CSRFToken
	}
	return resp, data
}

func (b *browser) mustLogin(email, password string) {
	b.t.Helper()
	if resp, data := b.login(email, password); resp.StatusCode != http.StatusOK {
		b.t.Fatalf("login %s: %d %s", email, resp.StatusCode, data)
	}
}

func status(t *testing.T, resp *http.Response, data []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, want, data)
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	a := newApp(t)
	testutil.UserWithPassword(t, a.pool, "dev@example.test", userPassword, false, false)
	b := a.browser()

	resp, data := b.login("DEV@example.test", userPassword) // email is case insensitive
	status(t, resp, data, http.StatusOK)
	if strings.Contains(string(data), "password_hash") || strings.Contains(string(data), "$argon2") || strings.Contains(string(data), userPassword) {
		t.Errorf("login response leaks credentials: %s", data)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "sandpost_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
		t.Fatalf("session cookie attributes: %+v", cookie)
	}
	if len(cookie.Value) < 40 {
		t.Errorf("session value too short: %d", len(cookie.Value))
	}
	var storedID int
	if err := a.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM sessions WHERE position($1 in encode(id, 'escape')) > 0", cookie.Value).Scan(&storedID); err != nil || storedID != 0 {
		t.Errorf("the raw cookie value must not be stored: %d %v", storedID, err)
	}

	var me struct {
		User      struct{ Email string } `json:"user"`
		AuthKind  string                 `json:"auth_kind"`
		CSRFToken string                 `json:"csrf_token"`
	}
	resp, data = b.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusOK)
	_ = json.Unmarshal(data, &me)
	if me.User.Email != "dev@example.test" || me.AuthKind != "session" || me.CSRFToken != b.csrf {
		t.Errorf("me: %s", data)
	}

	// Logout needs the CSRF token like any cookie authenticated write.
	resp, data = b.do(http.MethodPost, api.V1Prefix+"/auth/logout", map[string]any{}, "X-CSRF-Token", "")
	status(t, resp, data, http.StatusForbidden)
	resp, data = b.do(http.MethodPost, api.V1Prefix+"/auth/logout", map[string]any{})
	status(t, resp, data, http.StatusNoContent)
	resp, data = b.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusUnauthorized)

	// The old cookie is dead server side, not only removed from the jar.
	u, _ := url.Parse(a.srv.URL)
	b.c.Jar.SetCookies(u, []*http.Cookie{cookie})
	resp, data = b.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusUnauthorized)
}

func TestCSRFProtection(t *testing.T) {
	a := newApp(t)
	testutil.UserWithPassword(t, a.pool, "admin2@example.test", userPassword, true, false)
	b := a.browser()
	b.mustLogin("admin2@example.test", userPassword)

	create := func(csrf string) int {
		resp, _ := b.do(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": "x" + csrf[:min(3, len(csrf))]}, "X-CSRF-Token", csrf)
		return resp.StatusCode
	}
	if got := create("nope"); got != http.StatusForbidden {
		t.Errorf("wrong CSRF token: %d", got)
	}
	// A token of another session is not valid either.
	other := a.browser()
	other.mustLogin("admin2@example.test", userPassword)
	if got := create(other.csrf); got != http.StatusForbidden {
		t.Errorf("CSRF token of another session: %d", got)
	}
	if got := create(b.csrf); got != http.StatusCreated {
		t.Errorf("valid CSRF token: %d", got)
	}
	resp, data := b.do(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": "y"}, "X-CSRF-Token", "")
	status(t, resp, data, http.StatusForbidden)
	if !strings.Contains(string(data), "csrf_invalid") {
		t.Errorf("error code: %s", data)
	}
	// Reads do not need it, and bearer tokens never do.
	resp, data = b.do(http.MethodGet, api.V1Prefix+"/projects", nil)
	status(t, resp, data, http.StatusOK)
	a.mustJSON(http.MethodPost, api.V1Prefix+"/projects", map[string]string{"name": "via token"}, http.StatusCreated, nil)
	// A form post (what a cross site attacker can send) is refused before auth even matters.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, a.srv.URL+api.V1Prefix+"/projects", strings.NewReader("name=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fresp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = fresp.Body.Close()
	if fresp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", fresp.StatusCode)
	}
}

func TestForcedPasswordChange(t *testing.T) {
	a := newApp(t)
	var created struct {
		ID int64 `json:"id"`
	}
	a.mustJSON(http.MethodPost, api.V1Prefix+"/users",
		map[string]any{"email": "new@example.test", "password": userPassword}, http.StatusCreated, &created)

	b, second := a.browser(), a.browser()
	b.mustLogin("new@example.test", userPassword)
	second.mustLogin("new@example.test", userPassword)

	// Everything except me, password and logout is blocked with a stable code.
	for _, path := range []string{"/projects", "/users/me/tokens"} {
		resp, data := b.do(http.MethodGet, api.V1Prefix+path, nil)
		status(t, resp, data, http.StatusForbidden)
		if !strings.Contains(string(data), "password_change_required") {
			t.Errorf("%s: %s", path, data)
		}
	}
	resp, data := b.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusOK)
	if !strings.Contains(string(data), `"must_change_password":true`) {
		t.Errorf("me must expose the flag: %s", data)
	}

	pw := func(cur, next string) (*http.Response, []byte) {
		return b.do(http.MethodPost, api.V1Prefix+"/auth/password", map[string]string{"current_password": cur, "new_password": next})
	}
	resp, data = pw("wrong password!!", otherPass)
	status(t, resp, data, http.StatusBadRequest)
	resp, data = pw(userPassword, "short")
	status(t, resp, data, http.StatusBadRequest)
	resp, data = pw(userPassword, userPassword)
	status(t, resp, data, http.StatusBadRequest)
	resp, data = pw(userPassword, otherPass)
	status(t, resp, data, http.StatusNoContent)

	resp, data = b.do(http.MethodGet, api.V1Prefix+"/projects", nil)
	status(t, resp, data, http.StatusOK)
	// The other session of the same user ended, the old password is gone.
	resp, data = second.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusUnauthorized)
	fresh := a.browser()
	if resp, data := fresh.login("new@example.test", userPassword); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password still works: %d %s", resp.StatusCode, data)
	}
	fresh.mustLogin("new@example.test", otherPass)
}

func TestPasswordChangeNeedsASession(t *testing.T) {
	a := newApp(t)
	resp, data := a.do(http.MethodPost, api.V1Prefix+"/auth/password",
		map[string]string{"current_password": "x", "new_password": otherPass})
	status(t, resp, data, http.StatusForbidden)
}

func TestFailedLoginsLookAlikeAndAreLimited(t *testing.T) {
	a := newApp(t)
	testutil.UserWithPassword(t, a.pool, "ok@example.test", userPassword, false, false)
	off := testutil.UserWithPassword(t, a.pool, "off@example.test", userPassword, false, false)
	a.mustJSON(http.MethodPatch, api.V1Prefix+"/users/"+itoa64(off), map[string]any{"disabled": true}, http.StatusOK, nil)

	b := a.browser()
	var bodies []string
	for _, c := range [][2]string{
		{"ok@example.test", "wrong password!!"}, {"ghost@example.test", userPassword},
		{"off@example.test", userPassword}, {"", ""}, {strings.Repeat("a", 400), "x"},
	} {
		resp, data := b.login(c[0], c[1])
		status(t, resp, data, http.StatusUnauthorized)
		bodies = append(bodies, string(data))
	}
	for _, body := range bodies[1:] {
		if body != bodies[0] {
			t.Errorf("failures must be indistinguishable: %q vs %q", body, bodies[0])
		}
	}

	// The account is limited after repeated failures, even with the right password.
	for range 10 {
		b.login("victim@example.test", "nope nope nope")
	}
	resp, data := b.login("victim@example.test", "nope nope nope")
	status(t, resp, data, http.StatusTooManyRequests)
	if resp.Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
}

func TestSecureCookieMode(t *testing.T) {
	a := newApp(t, func(d *api.Deps) { d.CookieSecure = true })
	testutil.UserWithPassword(t, a.pool, "s@example.test", userPassword, false, false)
	resp, data := a.browser().login("s@example.test", userPassword)
	status(t, resp, data, http.StatusOK)
	cs := resp.Cookies()
	if len(cs) != 1 || cs[0].Name != "__Host-sandpost_session" || !cs[0].Secure || !cs[0].HttpOnly || cs[0].Path != "/" {
		t.Errorf("cookies: %+v", cs)
	}
}

func TestSessionsEndWhenUserIsDisabledOrExpired(t *testing.T) {
	a := newApp(t)
	uid := testutil.UserWithPassword(t, a.pool, "temp@example.test", userPassword, false, false)
	b, c := a.browser(), a.browser()
	b.mustLogin("temp@example.test", userPassword)
	c.mustLogin("temp@example.test", userPassword)
	tok := a.userToken(uid, "read")

	if _, err := a.pool.Exec(context.Background(), "UPDATE sessions SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	resp, data := b.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusUnauthorized)

	a.mustJSONAs(tok, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusOK, nil)
	a.mustJSON(http.MethodPatch, api.V1Prefix+"/users/"+itoa64(uid), map[string]any{"disabled": true}, http.StatusOK, nil)
	a.mustJSONAs(tok, http.MethodGet, api.V1Prefix+"/projects", nil, http.StatusUnauthorized, nil)
	resp, data = c.do(http.MethodGet, api.V1Prefix+"/auth/me", nil)
	status(t, resp, data, http.StatusUnauthorized)
}
