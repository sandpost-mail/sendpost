package ciclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "7", "spp_secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name, url, project, token string
		ok                        bool
	}{
		{"ok", "http://localhost:8080", "1", "t", true},
		{"prefix path", "https://x.test/mail/", "12", "t", true},
		{"no url", "", "1", "t", false},
		{"bad scheme", "ftp://x", "1", "t", false},
		{"no host", "http://", "1", "t", false},
		{"no project", "http://x", "", "t", false},
		{"project not numeric", "http://x", "demo", "t", false},
		{"project zero", "http://x", "0", "t", false},
		{"no token", "http://x", "1", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.url, tc.project, tc.token)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v is not ErrInvalid", err)
			}
		})
	}
}

func TestWaitRequest(t *testing.T) {
	var gotPath, gotAuth string
	var gotQuery map[string][]string
	var gotHeader string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotQuery, gotHeader = r.URL.Path, r.Header.Get("Authorization"), r.URL.Query(), r.Header.Get("X-Run-Id")
		_, _ = w.Write([]byte(`{"messages":[{"id":5,"subject":"Hi"}],"timed_out":false}`))
	})
	res, raw, err := c.Wait(context.Background(), WaitParams{Query: "subject:welcome back", Run: "r 1&x", AfterID: 4, Count: 2, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/_/api/v1/projects/7/wait" || gotAuth != "Bearer spp_secret" || gotHeader != "" {
		t.Fatalf("path %q auth %q run header %q", gotPath, gotAuth, gotHeader)
	}
	want := map[string]string{"q": "subject:welcome back", "run": "r 1&x", "after_id": "4", "count": "2", "timeout": "20"}
	for k, v := range want {
		if len(gotQuery[k]) != 1 || gotQuery[k][0] != v {
			t.Errorf("query %s = %v, want %q", k, gotQuery[k], v)
		}
	}
	if len(res.Messages) != 1 || res.Messages[0].ID != 5 || res.TimedOut || !strings.Contains(string(raw), `"timed_out":false`) {
		t.Fatalf("result %+v raw %s", res, raw)
	}
}

func TestWaitOmitsEmptyParams(t *testing.T) {
	var rawQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"messages":[],"timed_out":true}`))
	})
	res, _, err := c.Wait(context.Background(), WaitParams{Count: 1, Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || rawQuery != "count=1&timeout=0" {
		t.Fatalf("timed_out %v query %q", res.TimedOut, rawQuery)
	}
}

func TestWaitParamValidation(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("must not call the server") })
	tests := []struct {
		name string
		p    WaitParams
	}{
		{"count 0", WaitParams{Count: 0}},
		{"count 51", WaitParams{Count: 51}},
		{"negative timeout", WaitParams{Count: 1, Timeout: -time.Second}},
		{"timeout above cap", WaitParams{Count: 1, Timeout: 121 * time.Second}},
		{"negative after", WaitParams{Count: 1, AfterID: -1}},
		{"empty run is fine but control char is not", WaitParams{Count: 1, Run: "a\nb"}},
		{"run too long", WaitParams{Count: 1, Run: strings.Repeat("x", 101)}},
		{"run padded", WaitParams{Count: 1, Run: " x"}},
		{"run invalid utf8", WaitParams{Count: 1, Run: "\xff"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.Wait(context.Background(), tc.p); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSubSecondTimeoutRoundsUp(t *testing.T) {
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("timeout")
		_, _ = w.Write([]byte(`{"messages":[],"timed_out":true}`))
	})
	if _, _, err := c.Wait(context.Background(), WaitParams{Count: 1, Timeout: 1500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got != "2" {
		t.Fatalf("timeout = %q, want 2", got)
	}
}

func TestExtractRequest(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(`{"message_id":9,"links":["https://a.test/v?t=1"],"codes":[{"value":"482913","kind":"keyword"}]}`))
	})
	ex, raw, err := c.Extract(context.Background(), 9, ExtractParams{LinkContains: "verify", CodePattern: `(\d{6})`})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/_/api/v1/projects/7/messages/9/extract" || gotQuery["link"][0] != "verify" || gotQuery["code_pattern"][0] != `(\d{6})` {
		t.Fatalf("path %q query %v", gotPath, gotQuery)
	}
	if ex.MessageID != 9 || ex.Links[0] != "https://a.test/v?t=1" || ex.Codes[0].Value != "482913" || len(raw) == 0 {
		t.Fatalf("extraction %+v", ex)
	}
	if _, _, err := c.Extract(context.Background(), 0, ExtractParams{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("message id 0: %v", err)
	}
}

func TestBasePathPrefix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"messages":[],"timed_out":true}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL+"/mail/", "3", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Wait(context.Background(), WaitParams{Count: 1}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/mail/_/api/v1/projects/3/wait" {
		t.Fatalf("path %q", gotPath)
	}
}

func TestAPIErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, `{"error":"unauthenticated"}`, "authentication failed"},
		{"forbidden", 403, `{"error":"forbidden"}`, "not allowed"},
		{"not found", 404, `{"error":"not found"}`, "project or message not found"},
		{"bad request", 400, `{"error":"count must be between 1 and 50"}`, "count must be between 1 and 50"},
		{"server html", 502, `<html>bad gateway</html>`, "502"},
		{"empty body", 500, ``, "500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, _, err := c.Wait(context.Background(), WaitParams{Count: 1})
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status {
				t.Fatalf("err = %v, want APIError %d", err, tc.status)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "spp_secret") {
				t.Fatal("error leaks the token")
			}
		})
	}
}

func TestMalformedSuccessBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) })
	if _, _, err := c.Wait(context.Background(), WaitParams{Count: 1}); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := c.Extract(context.Background(), 1, ExtractParams{}); err == nil {
		t.Fatal("want decode error")
	}
}

func TestTransportErrorDoesNotLeakToken(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "1", "spp_secret")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Wait(context.Background(), WaitParams{Count: 1})
	if err == nil || strings.Contains(err.Error(), "spp_secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := c.Wait(ctx, WaitParams{Count: 1, Timeout: 30 * time.Second}); err == nil {
		t.Fatal("want error")
	}
}

func TestClientDeadlineExceedsServerTimeout(t *testing.T) {
	// The server answers after 1.2s although the poll was asked for 0s: a
	// client deadline equal to the timeout alone would have cut it.
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"messages":[],"timed_out":true}`))
	})
	if _, _, err := c.Wait(context.Background(), WaitParams{Count: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestValidRunID(t *testing.T) {
	for _, ok := range []string{"1", "123-1", "run:abc", "ünï", strings.Repeat("x", 100)} {
		if err := ValidateRunID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a\tb", "x ", strings.Repeat("x", 101)} {
		if err := ValidateRunID(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
