package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// authStyle is how a test client presents its token.
type authStyle int

const (
	authBearer authStyle = iota
	authBasic
	authNone
)

// compatResp is a response of the compatible API.
type compatResp struct {
	*http.Response
	body []byte
}

func (r compatResp) text() string { return string(r.body) }

// compat sends a request to the compatible API. Like most Mailpit clients it
// sends no Content-Type on bodyless requests.
func (a *app) compat(style authStyle, token, method, path, body string) compatResp {
	a.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	switch style {
	case authBearer:
		req.Header.Set("Authorization", "Bearer "+token)
	case authBasic:
		req.SetBasicAuth("any-username", token)
	case authNone:
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return compatResp{Response: resp, body: data}
}

// mp is the common case: a Basic auth request, as Mailpit clients send them.
func (a *app) mp(token, method, path, body string) compatResp {
	a.t.Helper()
	return a.compat(authBasic, token, method, path, body)
}

// mustMP sends a request and fails unless the status is want.
func (a *app) mustMP(token, method, path, body string, want int) compatResp {
	a.t.Helper()
	r := a.mp(token, method, path, body)
	if r.StatusCode != want {
		a.t.Fatalf("%s %s: status %d want %d: %s", method, path, r.StatusCode, want, r.body)
	}
	return r
}

// decode unmarshals a JSON body into v.
func (r compatResp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, r.body)
	}
}

// checkShape validates a JSON response against the vendored swagger.
func checkShape(t *testing.T, sw *swagger, r compatResp, method, swaggerPath string) {
	t.Helper()
	schema := sw.schemaOf(method, swaggerPath, "200")
	if schema == nil {
		t.Fatalf("%s %s has no JSON schema in the contract", method, swaggerPath)
	}
	var v any
	r.decode(t, &v)
	if problems := sw.validate("$", v, schema); len(problems) > 0 {
		t.Errorf("%s %s does not match the Mailpit contract:\n  %s\nbody: %s", method, swaggerPath, strings.Join(problems, "\n  "), r.body)
	}
}
