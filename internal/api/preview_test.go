package api_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// previewFixture is a message with an inline image in project one, and a
// second project with a message of its own.
type previewFixture struct {
	w        *world
	htmlURL  string
	imageURL string
}

func newPreviewFixture(t *testing.T) previewFixture {
	t.Helper()
	w := newWorld(t)
	var out struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/preview-url", w.p1.mid), nil, http.StatusOK, &out)
	if !strings.Contains(out.URL, "?grant=") || time.Until(out.ExpiresAt) > 10*time.Minute || time.Until(out.ExpiresAt) <= 0 {
		t.Fatalf("preview url: %+v", out)
	}
	// Anonymous fetch, as the opaque origin iframe does.
	resp, body := w.a.doAs("", http.MethodGet, out.URL, nil)
	status(t, resp, body, http.StatusOK)
	m := regexp.MustCompile(`/_/api/v1/projects/\d+/messages/\d+/attachments/1\?grant=[0-9]+\.[A-Za-z0-9_-]+`).FindString(string(body))
	if m == "" || strings.Contains(string(body), "cid:logo123") {
		t.Fatalf("cid references must point at signed attachment urls: %s", body)
	}
	return previewFixture{w: w, htmlURL: out.URL, imageURL: m}
}

func TestSignedPreviewServesWithoutCredentials(t *testing.T) {
	f := newPreviewFixture(t)
	a := f.w.a
	resp, body := a.doAs("", http.MethodGet, f.htmlURL, nil)
	status(t, resp, body, http.StatusOK)
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP: %s", csp)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", resp.Header)
	}
	resp, body = a.doAs("", http.MethodGet, f.imageURL, nil)
	status(t, resp, body, http.StatusOK)
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("attachment headers: %v", resp.Header)
	}
}

func TestSignedPreviewCannotBeReusedElsewhere(t *testing.T) {
	f := newPreviewFixture(t)
	w, a := f.w, f.w.a
	grant := f.htmlURL[strings.Index(f.htmlURL, "?"):]
	own := fmt.Sprintf("/projects/%d/messages/%d", w.p1.pid, w.p1.mid)
	foreign := fmt.Sprintf("/projects/%d/messages/%d", w.p2.pid, w.p2.mid)
	mixedProject := fmt.Sprintf("/projects/%d/messages/%d", w.p2.pid, w.p1.mid)
	mixedMessage := fmt.Sprintf("/projects/%d/messages/%d", w.p1.pid, w.p2.mid)

	tests := []struct {
		name string
		path string
		want int
	}{
		{"same ids and resource", own + "/html" + grant, http.StatusOK},
		{"another project and message", foreign + "/html" + grant, http.StatusNotFound},
		{"another project, same message id", mixedProject + "/html" + grant, http.StatusNotFound},
		{"same project, another message", mixedMessage + "/html" + grant, http.StatusNotFound},
		{"html grant on an attachment", own + "/attachments/1" + grant, http.StatusNotFound},
		{"html grant on the raw source", own + "/raw" + grant, http.StatusUnauthorized},
		{"html grant on the message detail", own + grant, http.StatusUnauthorized},
		{"html grant on the project", fmt.Sprintf("/projects/%d%s", w.p1.pid, grant), http.StatusUnauthorized},
		{"expired", own + "/html?grant=1.AAAA", http.StatusNotFound},
		{"garbage", own + "/html?grant=nonsense", http.StatusNotFound},
		{"missing signature", own + "/html?grant=9999999999.", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := a.doAs("", http.MethodGet, v1+tc.path, nil)
			if resp.StatusCode != tc.want {
				t.Errorf("status %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}

	// The grant of an attachment is bound to its ordinal.
	other := strings.Replace(f.imageURL, "/attachments/1?", "/attachments/0?", 1)
	resp, body := a.doAs("", http.MethodGet, other, nil)
	expectStatus(t, resp, string(body), http.StatusNotFound)

	// The grant is only honoured for GET, there is no write route to reach.
	resp, body = a.doAs("", http.MethodPost, f.htmlURL, map[string]any{})
	expectStatus(t, resp, string(body), http.StatusNotFound)
}

func TestPreviewURLNeedsReadAccess(t *testing.T) {
	w := newWorld(t)
	url := pp(w.p1.pid, "/messages/%d/preview-url", w.p1.mid)
	w.a.mustJSONAs("", http.MethodGet, url, nil, http.StatusUnauthorized, nil)
	w.a.mustJSONAs(w.bob, http.MethodGet, url, nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.alice, http.MethodGet, pp(w.p1.pid, "/messages/%d/preview-url", w.p2.mid), nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.proj1Read, http.MethodGet, url, nil, http.StatusOK, nil)
	// A message without an HTML part has nothing to preview.
	textOnly := w.a.capture(w.p1.pid, "simple_text.eml")
	w.a.mustJSONAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/preview-url", textOnly), nil, http.StatusNotFound, nil)
}

func TestAuthenticatedHTMLAndAttachmentStillWork(t *testing.T) {
	w := newWorld(t)
	resp, body := w.a.doAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/html", w.p1.mid), nil)
	status(t, resp, body, http.StatusOK)
	resp, body = w.a.doAs(w.vera, http.MethodGet, pp(w.p1.pid, "/messages/%d/attachments/0", w.p1.mid), nil)
	status(t, resp, body, http.StatusOK)
	resp, body = w.a.doAs(w.bob, http.MethodGet, pp(w.p1.pid, "/messages/%d/attachments/0", w.p1.mid), nil)
	status(t, resp, body, http.StatusNotFound)
}

// TestEventStreamAccess checks that the stream authorizes before it
// subscribes, and that a browser session stream ends with the session.
func TestEventStreamAccess(t *testing.T) {
	w := newWorld(t)
	events := pp(w.p1.pid, "/events")
	w.a.mustJSONAs("", http.MethodGet, events, nil, http.StatusUnauthorized, nil)
	w.a.mustJSONAs(w.bob, http.MethodGet, events, nil, http.StatusNotFound, nil)

	uid := testutil.UserWithPassword(t, w.a.pool, "sse@example.test", userPassword, false, false)
	g := testutil.Group(t, w.a.pool, "sse-group")
	testutil.Member(t, w.a.pool, g, uid)
	testutil.Assign(t, w.a.pool, w.p1.pid, g, "viewer")
	b := w.a.browser()
	b.mustLogin("sse@example.test", userPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.a.srv.URL+events, nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(substr string) {
		t.Helper()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %q", substr)
				}
				if strings.Contains(l, substr) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q", substr)
			}
		}
	}
	waitFor("event: ready")

	// Shorten the session: the server must end the stream at expiry.
	if _, err := w.a.pool.Exec(context.Background(), "UPDATE sessions SET expires_at = now() + interval '1 second' WHERE user_id = $1", uid); err != nil {
		t.Fatal(err)
	}
	// The stream already captured the old expiry (24h): a fresh connection picks up the new one.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.a.srv.URL+events, nil)
	resp2, err := b.c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(resp2.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: message.created") {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its session")
	}
}
