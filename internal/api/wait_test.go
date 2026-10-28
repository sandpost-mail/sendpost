package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
)

type waitResult struct {
	Messages []messages.Summary `json:"messages"`
	TimedOut bool               `json:"timed_out"`
}

type extraction struct {
	MessageID int64    `json:"message_id"`
	Links     []string `json:"links"`
	Codes     []struct {
		Value string `json:"value"`
		Kind  string `json:"kind"`
	} `json:"codes"`
}

// send ingests a message with an optional X-Run-Id header and returns its id.
func (a *app) send(pid int64, run, subject, body string) int64 {
	a.t.Helper()
	hdr := ""
	if run != "" {
		hdr = "X-Run-Id: " + run + "\r\n"
	}
	raw := fmt.Sprintf("From: app@example.test\r\nTo: u@example.test\r\nSubject: %s\r\n%s\r\n%s", subject, hdr, body)
	id, err := a.ing.Ingest(context.Background(), ingest.Input{ProjectID: pid, EnvelopeTo: []string{"u@example.test"}, Raw: []byte(raw)})
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

// getH is doAs with extra request headers.
func (a *app) getH(token, path string, hdr map[string]string) (*http.Response, []byte) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.srv.URL+path, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, []byte(sb.String())
}

func waitIDs(r waitResult) []int64 {
	out := []int64{}
	for _, m := range r.Messages {
		out = append(out, m.ID)
	}
	return out
}

func TestWaitReturnsExistingMatchesAndIsolatesRuns(t *testing.T) {
	a := newApp(t)
	p1, p2 := a.project("one"), a.project("two")
	m1 := a.send(p1.ID, "run-1", "welcome", "hi")
	m2 := a.send(p1.ID, "run-2", "welcome", "hi")
	foreign := a.send(p2.ID, "run-1", "welcome", "hi")

	var r waitResult
	a.mustJSON(http.MethodGet, pp(p1.ID, "/wait?timeout=5&q=subject:welcome"), nil, http.StatusOK, &r)
	if r.TimedOut || len(r.Messages) != 2 || r.Messages[0].ID != m1 || r.Messages[1].ID != m2 {
		t.Fatalf("all: %+v", r)
	}

	// X-Run-Id header, run parameter and both together.
	resp, body := a.getH(a.token, pp(p1.ID, "/wait?timeout=5"), map[string]string{"X-Run-Id": "run-2"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf(`"id":%d`, m2)) || strings.Contains(string(body), fmt.Sprintf(`"id":%d`, m1)) {
		t.Fatalf("header: %d %s", resp.StatusCode, body)
	}
	a.mustJSON(http.MethodGet, pp(p1.ID, "/wait?timeout=5&run=run-1"), nil, http.StatusOK, &r)
	if len(r.Messages) != 1 || r.Messages[0].ID != m1 {
		t.Fatalf("run param: %+v", r)
	}
	resp, _ = a.getH(a.token, pp(p1.ID, "/wait?timeout=5&run=run-1"), map[string]string{"X-Run-Id": "run-1"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("equal header and param: %d", resp.StatusCode)
	}
	resp, _ = a.getH(a.token, pp(p1.ID, "/wait?timeout=5&run=run-1"), map[string]string{"X-Run-Id": "run-2"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("conflicting run ids: %d", resp.StatusCode)
	}

	// Another project's mail with the same run id never shows up, on either side.
	a.mustJSON(http.MethodGet, pp(p1.ID, "/wait?timeout=0&run=run-1&count=2"), nil, http.StatusOK, &r)
	if !r.TimedOut || len(r.Messages) != 1 || r.Messages[0].ID != m1 {
		t.Fatalf("run-1 in p1 only has one message: %+v (foreign %d)", r, foreign)
	}
	a.mustJSON(http.MethodGet, pp(p2.ID, "/wait?timeout=0&run=run-1"), nil, http.StatusOK, &r)
	if len(r.Messages) != 1 || r.Messages[0].ID != foreign {
		t.Fatalf("p2: %+v", r)
	}

	// after_id resumes where the last answer ended.
	a.mustJSON(http.MethodGet, pp(p1.ID, "/wait?timeout=0&after_id=%d", m1), nil, http.StatusOK, &r)
	if got := waitIDs(r); len(got) != 1 || got[0] != m2 {
		t.Fatalf("after_id: %v", got)
	}
}

func TestWaitBlocksUntilAMatchingMessageArrives(t *testing.T) {
	a := newApp(t)
	p, other := a.project("one"), a.project("two")
	a.send(p.ID, "run-9", "old", "x")

	done := make(chan waitResult, 1)
	go func() {
		var r waitResult
		a.mustJSON(http.MethodGet, pp(p.ID, "/wait?timeout=20&run=run-9&count=2"), nil, http.StatusOK, &r)
		done <- r
	}()
	time.Sleep(300 * time.Millisecond)
	a.send(other.ID, "run-9", "foreign", "x") // wakes nobody of this project
	a.send(p.ID, "run-8", "other run", "x")   // wakes the poll, does not match
	select {
	case r := <-done:
		t.Fatalf("returned too early: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	want := a.send(p.ID, "run-9", "new", "x")
	select {
	case r := <-done:
		if r.TimedOut || len(r.Messages) != 2 || r.Messages[1].ID != want {
			t.Fatalf("got %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake up")
	}
}

func TestWaitTimesOut(t *testing.T) {
	a := newApp(t)
	p := a.project("one")
	start := time.Now()
	var r waitResult
	a.mustJSON(http.MethodGet, pp(p.ID, "/wait?timeout=1"), nil, http.StatusOK, &r)
	if !r.TimedOut || r.Messages == nil || len(r.Messages) != 0 {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second {
		t.Errorf("waited %v", d)
	}
}

func TestWaitRejectsBadParameters(t *testing.T) {
	a := newApp(t)
	p := a.project("one")
	for _, q := range []string{
		"timeout=-1", "timeout=121", "timeout=x", "count=0", "count=51", "after_id=-1", "after_id=x",
		"q=date:nope", "run=" + url.QueryEscape("a\nb"), "run=" + strings.Repeat("x", 101),
	} {
		resp, body := a.do(http.MethodGet, pp(p.ID, "/wait?%s", q), nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, resp.StatusCode, body)
		}
	}
}

func TestWaitAuthorization(t *testing.T) {
	w := newWorld(t)
	path := pp(w.p1.pid, "/wait?timeout=0")
	for name, tok := range map[string]string{"viewer": w.vera, "member": w.alice, "project token": w.proj1Read, "read scope": w.ownerRead} {
		if resp, body := w.a.doAs(tok, http.MethodGet, path, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, _ := w.a.doAs("", http.MethodGet, path, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
	// Tokens of project 1 and users without a role must not see project 2, not even its run ids.
	w.a.send(w.p2.pid, "secret-run", "p2 only", "x")
	foreign := pp(w.p2.pid, "/wait?timeout=0&run=secret-run")
	for name, tok := range map[string]string{"project token p1": w.proj1Write, "viewer p1": w.vera, "no role": w.nobody} {
		resp, body := w.a.doAs(tok, http.MethodGet, foreign, nil)
		if resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "p2 only") {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	// The same call as a legitimate p2 caller finds it, so the 404 above is isolation, not an empty result.
	if resp, body := w.a.doAs(w.bob, http.MethodGet, foreign, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "p2 only") {
		t.Errorf("owner of p2: %d %s", resp.StatusCode, body)
	}
}

func TestExtract(t *testing.T) {
	a := newApp(t)
	p := a.project("one")
	body := "Hi,\r\nYour verification code is 482 913.\r\nConfirm: https://app.example.test/verify?token=abc123 or https://app.example.test/help\r\n"
	id := a.send(p.ID, "", "Verify", body)

	var e extraction
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/extract", id), nil, http.StatusOK, &e)
	if e.MessageID != id || len(e.Codes) != 1 || e.Codes[0].Value != "482913" || e.Codes[0].Kind != "keyword" || len(e.Links) != 2 {
		t.Fatalf("%+v", e)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/extract?link=VERIFY", id), nil, http.StatusOK, &e)
	if len(e.Links) != 1 || e.Links[0] != "https://app.example.test/verify?token=abc123" {
		t.Fatalf("link filter: %+v", e.Links)
	}
	a.mustJSON(http.MethodGet, pp(p.ID, "/messages/%d/extract?code_pattern=%s", id, url.QueryEscape(`token=(\w+)`)), nil, http.StatusOK, &e)
	if len(e.Codes) != 1 || e.Codes[0].Value != "abc123" || e.Codes[0].Kind != "custom" {
		t.Fatalf("custom: %+v", e.Codes)
	}
	// Empty results are arrays, never null.
	empty := a.send(p.ID, "", "none", "nothing here")
	_, raw := a.do(http.MethodGet, pp(p.ID, "/messages/%d/extract", empty), nil)
	if !strings.Contains(string(raw), `"links":[]`) || !strings.Contains(string(raw), `"codes":[]`) {
		t.Errorf("empty shape: %s", raw)
	}
	for _, q := range []string{"code_pattern=" + url.QueryEscape("("), "code_pattern=" + strings.Repeat("a", 201)} {
		if resp, b := a.do(http.MethodGet, pp(p.ID, "/messages/%d/extract?%s", id, q), nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, resp.StatusCode, b)
		}
	}
	if resp, _ := a.do(http.MethodGet, pp(p.ID, "/messages/999999/extract"), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing message: %d", resp.StatusCode)
	}
}

func TestExtractIsolatedPerTenant(t *testing.T) {
	w := newWorld(t)
	secret := w.a.send(w.p2.pid, "", "secret", "Your code is 777888 https://p2.example.test/x")
	// A message of project 2 addressed through project 1, and through project 2 without a role.
	for name, c := range map[string]struct{ tok, path string }{
		"p1 owner via p1":  {w.owner1, pp(w.p1.pid, "/messages/%d/extract", secret)},
		"p1 token via p2":  {w.proj1Read, pp(w.p2.pid, "/messages/%d/extract", secret)},
		"viewer p1 via p2": {w.vera, pp(w.p2.pid, "/messages/%d/extract", secret)},
	} {
		resp, body := w.a.doAs(c.tok, http.MethodGet, c.path, nil)
		if resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "777888") || strings.Contains(string(body), "p2.example") {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	var e extraction
	w.a.mustJSONAs(w.bob, http.MethodGet, pp(w.p2.pid, "/messages/%d/extract", secret), nil, http.StatusOK, &e)
	if len(e.Codes) != 1 || e.Codes[0].Value != "777888" {
		t.Fatalf("owner of p2: %+v", e)
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/extract", w.p1.mid), nil, http.StatusOK, &e)
}
