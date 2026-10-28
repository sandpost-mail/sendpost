package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI is a Sandpost stand-in that records the requests it got.
type fakeAPI struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []*http.Request
	wait     string // body of the wait route
	extract  string // body of the extract route
	status   int    // when not 0, every route answers with it
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		wait:    `{"messages":[{"id":11,"subject":"a"},{"id":12,"subject":"b"}],"timed_out":false}`,
		extract: `{"message_id":11,"links":["https://app.test/verify?t=1","https://app.test/help"],"codes":[{"value":"482913","kind":"keyword"},{"value":"111111","kind":"digits"}]}`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/wait"):
			_, _ = w.Write([]byte(f.wait))
		case strings.HasSuffix(r.URL.Path, "/extract"):
			_, _ = w.Write([]byte(f.extract))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) last(t *testing.T) *http.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeAPI) vars(extra map[string]string) map[string]string {
	v := map[string]string{envURL: f.srv.URL, envProject: "3", envToken: "spp_tok"}
	for k, val := range extra {
		v[k] = val
	}
	return v
}

func runCI(t *testing.T, vars map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	e, out, errb := testEnv(vars)
	code = run(context.Background(), e, args)
	return code, out.String(), errb.String()
}

func TestWaitPlain(t *testing.T) {
	f := newFakeAPI(t)
	code, out, errs := runCI(t, f.vars(nil), "wait", "--query", "subject:hi", "--count", "2", "--timeout", "10")
	if code != exitOK || out != "11\n12\n" || errs != "" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
	}
	r := f.last(t)
	q := r.URL.Query()
	if r.URL.Path != "/_/api/v1/projects/3/wait" || r.Header.Get("Authorization") != "Bearer spp_tok" ||
		q.Get("q") != "subject:hi" || q.Get("count") != "2" || q.Get("timeout") != "10" || q.Has("run") {
		t.Fatalf("request %s auth %q", r.URL, r.Header.Get("Authorization"))
	}
}

func TestWaitJSONAndTimeout(t *testing.T) {
	f := newFakeAPI(t)
	f.wait = `{"messages":[{"id":5}],"timed_out":true}`
	code, out, errs := runCI(t, f.vars(nil), "wait", "--json", "--count", "2", "--timeout", "5s")
	if code != exitTimeout || out != f.wait+"\n" || !strings.Contains(errs, "timed out after 5s: 1 of 2") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
	}
	code, out, _ = runCI(t, f.vars(nil), "wait")
	if code != exitTimeout || out != "5\n" {
		t.Fatalf("plain timeout: code %d stdout %q", code, out)
	}
}

func TestWaitRunResolution(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		args    []string
		wantRun string
		wantSet bool
	}{
		{"none", nil, nil, "", false},
		{"sandpost env", map[string]string{envRunID: "r1", "GITHUB_RUN_ID": "gh"}, nil, "r1", true},
		{"github fallback", map[string]string{"GITHUB_RUN_ID": "gh"}, nil, "gh", true},
		{"gitlab fallback", map[string]string{"CI_PIPELINE_ID": "42"}, nil, "42", true},
		{"flag wins", map[string]string{envRunID: "r1"}, []string{"--run", "flag"}, "flag", true},
		{"empty flag disables", map[string]string{envRunID: "r1"}, []string{"--run="}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			args := append([]string{"wait"}, tc.args...)
			if code, _, errs := runCI(t, f.vars(tc.vars), args...); code != exitOK {
				t.Fatalf("code %d: %s", code, errs)
			}
			q := f.last(t).URL.Query()
			if q.Has("run") != tc.wantSet || q.Get("run") != tc.wantRun {
				t.Fatalf("run = %q (set %v), want %q", q.Get("run"), q.Has("run"), tc.wantRun)
			}
		})
	}
}

func TestCIUsageErrors(t *testing.T) {
	f := newFakeAPI(t)
	tests := []struct {
		name    string
		vars    map[string]string
		args    []string
		wantErr string
	}{
		{"no server", map[string]string{envURL: ""}, []string{"wait"}, "server URL is required"},
		{"no project", map[string]string{envProject: ""}, []string{"wait"}, "project id is required"},
		{"no token", map[string]string{envToken: ""}, []string{"wait"}, "SANDPOST_TOKEN"},
		{"flag server overrides env", map[string]string{envURL: "", envToken: ""}, []string{"wait", "--server", "http://x"}, "SANDPOST_TOKEN"},
		{"count 0", nil, []string{"wait", "--count", "0"}, "count must be between"},
		{"count 51", nil, []string{"wait", "--count", "51"}, "count must be between"},
		{"timeout 121", nil, []string{"wait", "--timeout", "121"}, "timeout must be between"},
		{"timeout junk", nil, []string{"wait", "--timeout", "soon"}, "invalid --timeout"},
		{"negative after", nil, []string{"wait", "--after-id", "-1"}, "after-id"},
		{"bad run", nil, []string{"wait", "--run", "a\nb"}, "run id"},
		{"bad env run", map[string]string{envRunID: " x"}, []string{"wait"}, "run id"},
		{"unknown flag", nil, []string{"wait", "--nope"}, "Usage: sandpost wait"},
		{"positional", nil, []string{"wait", "extra"}, `unexpected argument "extra"`},
		{"extract bad print", nil, []string{"extract", "--print", "x"}, "--print must be"},
		{"extract message 0", nil, []string{"extract", "--message", "0"}, "positive message id"},
		{"extract message plus query", nil, []string{"extract", "--message", "1", "--query", "x"}, "--query only applies when waiting"},
		{"extract message plus run", nil, []string{"extract", "--message", "1", "--run", "x"}, "--run only applies"},
		{"extract bad timeout", nil, []string{"extract", "--timeout", "x"}, "invalid --timeout"},
		{"extract bad run", map[string]string{envRunID: "a\tb"}, []string{"extract"}, "run id"},
		{"extract no token", map[string]string{envToken: ""}, []string{"extract", "--message", "1"}, "SANDPOST_TOKEN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := f.count()
			code, out, errs := runCI(t, f.vars(tc.vars), tc.args...)
			if code != exitUsage || out != "" || !strings.Contains(errs, tc.wantErr) {
				t.Fatalf("code %d stdout %q stderr %q, want usage error with %q", code, out, errs, tc.wantErr)
			}
			if strings.Contains(errs, "spp_tok") {
				t.Fatal("stderr leaks the token")
			}
			if f.count() != before {
				t.Fatal("usage errors must not reach the server")
			}
		})
	}
}

func TestCIHelp(t *testing.T) {
	for cmd, want := range map[string]string{"wait": "Usage: sandpost wait", "extract": "Usage: sandpost extract"} {
		code, out, _ := runCI(t, nil, cmd, "-h")
		if code != exitOK || !strings.Contains(out, want) || !strings.Contains(out, "Exit codes") {
			t.Fatalf("%s -h: code %d out %q", cmd, code, out)
		}
	}
	if _, out, _ := runCI(t, nil, "help"); !strings.Contains(out, "wait ") || !strings.Contains(out, "extract ") {
		t.Fatalf("main usage lacks the commands: %q", out)
	}
}

func TestCIServerErrors(t *testing.T) {
	for status, want := range map[int]string{401: "authentication failed", 403: "not allowed", 404: "not found", 400: "nope", 500: "nope"} {
		f := newFakeAPI(t)
		f.status = status
		for _, args := range [][]string{{"wait"}, {"extract", "--message", "1"}, {"extract"}} {
			code, out, errs := runCI(t, f.vars(nil), args...)
			if code != exitError || out != "" || !strings.Contains(errs, want) {
				t.Errorf("%d %v: code %d stdout %q stderr %q", status, args, code, out, errs)
			}
		}
	}
}

func TestCIUnreachableServer(t *testing.T) {
	code, _, errs := runCI(t, map[string]string{envURL: "http://127.0.0.1:1", envProject: "1", envToken: "spp_tok"}, "wait", "--timeout", "0")
	if code != exitError || !strings.Contains(errs, "request wait") || strings.Contains(errs, "spp_tok") {
		t.Fatalf("code %d stderr %q", code, errs)
	}
}

func TestExtractWaitsThenExtracts(t *testing.T) {
	f := newFakeAPI(t)
	code, out, errs := runCI(t, f.vars(map[string]string{envRunID: "r9"}), "extract", "--query", "subject:welcome")
	if code != exitOK || out != "482913\n" || errs != "" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want wait then extract", len(f.requests))
	}
	w, x := f.requests[0], f.requests[1]
	if w.URL.Query().Get("run") != "r9" || w.URL.Query().Get("count") != "1" || w.URL.Query().Get("q") != "subject:welcome" {
		t.Fatalf("wait request %s", w.URL)
	}
	if x.URL.Path != "/_/api/v1/projects/3/messages/11/extract" {
		t.Fatalf("extract path %s", x.URL.Path)
	}
}

func TestExtractLatest(t *testing.T) {
	f := newFakeAPI(t)
	if code, _, errs := runCI(t, f.vars(nil), "extract", "--latest"); code != exitOK {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := f.last(t).URL.Path; !strings.HasSuffix(got, "/messages/12/extract") {
		t.Fatalf("path %s, want the newest message", got)
	}
}

func TestExtractModes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
		query    map[string]string
	}{
		{"default is the first code", []string{"--message", "11"}, exitOK, "482913\n", "", nil},
		{"all codes", []string{"--message", "11", "--all"}, exitOK, "482913\n111111\n", "", nil},
		{"link filter implies link", []string{"--message", "11", "--link-contains", "verify"}, exitOK, "https://app.test/verify?t=1\n", "", map[string]string{"link": "verify"}},
		{"print link", []string{"--message", "11", "--print", "link", "--all"}, exitOK, "https://app.test/verify?t=1\nhttps://app.test/help\n", "", nil},
		{"pattern implies code", []string{"--message", "11", "--code-pattern", `(\d+)`, "--link-contains", "v"}, exitOK, "482913\n", "", map[string]string{"code_pattern": `(\d+)`}},
		{"explicit code with link filter", []string{"--message", "11", "--print", "code", "--link-contains", "v"}, exitOK, "482913\n", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			code, out, errs := runCI(t, f.vars(nil), append([]string{"extract"}, tc.args...)...)
			if code != tc.wantCode || out != tc.wantOut || !strings.Contains(errs, tc.wantErr) {
				t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
			}
			for k, v := range tc.query {
				if got := f.last(t).URL.Query().Get(k); got != v {
					t.Errorf("query %s = %q, want %q", k, got, v)
				}
			}
			if f.count() != 1 {
				t.Errorf("%d requests, --message must not wait", f.count())
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	f := newFakeAPI(t)
	code, out, _ := runCI(t, f.vars(nil), "extract", "--message", "11", "--json")
	want := `{"message_id":11,"value":"482913","links":["https://app.test/verify?t=1","https://app.test/help"],"codes":[{"value":"482913","kind":"keyword"},{"value":"111111","kind":"digits"}]}` + "\n"
	if code != exitOK || out != want {
		t.Fatalf("code %d stdout %q", code, out)
	}
	f.extract = `{"message_id":11,"links":null,"codes":null}`
	code, out, errs := runCI(t, f.vars(nil), "extract", "--message", "11", "--json")
	if code != exitNotFound || out != `{"message_id":11,"value":null,"links":[],"codes":[]}`+"\n" || !strings.Contains(errs, "has no code") {
		t.Fatalf("empty: code %d stdout %q stderr %q", code, out, errs)
	}
}

func TestExtractNothingFound(t *testing.T) {
	f := newFakeAPI(t)
	f.extract = `{"message_id":11,"links":[],"codes":[]}`
	code, out, errs := runCI(t, f.vars(nil), "extract", "--message", "11", "--print", "link")
	if code != exitNotFound || out != "" || !strings.Contains(errs, "message 11 has no link") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
	}
}

func TestExtractTimeout(t *testing.T) {
	f := newFakeAPI(t)
	f.wait = `{"messages":[],"timed_out":true}`
	code, out, errs := runCI(t, f.vars(nil), "extract", "--timeout", "1")
	if code != exitTimeout || out != "" || !strings.Contains(errs, "timed out after 1s") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errs)
	}
	if f.count() != 1 {
		t.Fatal("must not extract after a timeout")
	}
}
