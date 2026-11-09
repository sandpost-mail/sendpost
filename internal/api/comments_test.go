package api_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/activity"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
)

func commentsPath(t tenant) string { return pp(t.pid, "/messages/%d/comments", t.mid) }

func countComments(t *testing.T, w *world, pid int64) int {
	t.Helper()
	var n int
	if err := w.a.pool.QueryRow(context.Background(), "SELECT count(*) FROM comments WHERE project_id=$1", pid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Who may comment (messages.write, member and up) and who may read (viewer and up).
func TestCommentRoles(t *testing.T) {
	w := newWorld(t)
	path := commentsPath(w.p1)
	for _, act := range w.actors() {
		t.Run(act.name, func(t *testing.T) {
			want := map[bool]int{true: http.StatusCreated, false: http.StatusForbidden}[act.role.Allows(authz.CapWriteMessages)]
			wantRead := http.StatusOK
			if act.role == authz.RoleNone {
				want, wantRead = http.StatusNotFound, http.StatusNotFound
			}
			resp, body := w.a.doAs(act.token, http.MethodPost, path, map[string]string{"body": "hello from " + act.name})
			expectStatus(t, resp, string(body), want)
			resp, body = w.a.doAs(act.token, http.MethodGet, path, nil)
			expectStatus(t, resp, string(body), wantRead)
		})
	}
}

func TestCommentsAreListedOldestFirstWithAuthor(t *testing.T) {
	w := newWorld(t)
	path := commentsPath(w.p1)
	var first, second comments.Comment
	w.a.mustJSONAs(w.alice, http.MethodPost, path, map[string]string{"body": "  first\nline two  "}, http.StatusCreated, &first)
	w.a.mustJSONAs(w.owner1, http.MethodPost, path, map[string]string{"body": "second"}, http.StatusCreated, &second)
	if first.Body != "first\nline two" || first.Author != "alice@example.test" || first.UserID == nil || first.MessageID != w.p1.mid {
		t.Errorf("first = %+v", first)
	}
	// A viewer reads the thread.
	var page comments.Page
	w.a.mustJSONAs(w.vera, http.MethodGet, path+"?limit=1", nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("page 1 = %+v", page)
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, path+"?limit=1&cursor="+page.NextCursor, nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, path+"?cursor=zzz", nil, http.StatusBadRequest, nil)
	w.a.mustJSONAs(w.vera, http.MethodGet, path+"?limit=-1", nil, http.StatusBadRequest, nil)
}

func TestProjectTokenComments(t *testing.T) {
	w := newWorld(t)
	var c comments.Comment
	w.a.mustJSONAs(w.proj1Write, http.MethodPost, commentsPath(w.p1), map[string]string{"body": "from CI"}, http.StatusCreated, &c)
	if c.UserID != nil || !strings.HasPrefix(c.Author, "project token spp_") {
		t.Errorf("project token comment = %+v", c)
	}
	// The author can not be chosen by the caller.
	w.a.mustJSONAs(w.alice, http.MethodPost, commentsPath(w.p1), map[string]any{"body": "x", "author": "admin", "user_id": 1}, http.StatusBadRequest, nil)
}

func TestCommentContentIsPlainTextAndBounded(t *testing.T) {
	w := newWorld(t)
	path := commentsPath(w.p1)
	const xss = `<script>alert("x")</script> <img src=x onerror=alert(1)> & "q"`
	var c comments.Comment
	resp, data := w.a.doAs(w.alice, http.MethodPost, path, map[string]string{"body": xss})
	expectStatus(t, resp, string(data), http.StatusCreated)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, path, nil, http.StatusOK, &struct{ Items *[]comments.Comment }{})
	var page comments.Page
	w.a.mustJSONAs(w.vera, http.MethodGet, path, nil, http.StatusOK, &page)
	if c = page.Items[0]; c.Body != xss {
		t.Errorf("body not stored verbatim: %q", c.Body)
	}

	bad := map[string]string{
		"empty":      "",
		"blank":      " \n\t ",
		"too long":   strings.Repeat("a", comments.MaxBodyRunes+1),
		"nul":        "a\u0000b",
		"escape":     "a\u001bb",
		"multi long": strings.Repeat("é", comments.MaxBodyRunes+1),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			before := countComments(t, w, w.p1.pid)
			w.a.mustJSONAs(w.alice, http.MethodPost, path, map[string]string{"body": body}, http.StatusBadRequest, nil)
			if countComments(t, w, w.p1.pid) != before {
				t.Error("a rejected comment was stored")
			}
		})
	}
	// The limit counts characters, not bytes.
	w.a.mustJSONAs(w.alice, http.MethodPost, path, map[string]string{"body": strings.Repeat("é", comments.MaxBodyRunes)}, http.StatusCreated, nil)
	// A big body is refused before parsing.
	w.a.mustJSONAs(w.alice, http.MethodPost, path, map[string]string{"body": strings.Repeat("a", 70<<10)}, http.StatusBadRequest, nil)
}

func TestCommentsDoNotCrossProjects(t *testing.T) {
	w := newWorld(t)
	// Own project id with a message of another project: 404 and nothing stored.
	foreign := pp(w.p1.pid, "/messages/%d/comments", w.p2.mid)
	w.a.mustJSONAs(w.owner1, http.MethodPost, foreign, map[string]string{"body": "x"}, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.owner1, http.MethodGet, foreign, nil, http.StatusNotFound, nil)
	w.a.mustJSON(http.MethodPost, foreign, map[string]string{"body": "x"}, http.StatusNotFound, nil)
	if n := countComments(t, w, w.p2.pid) + countComments(t, w, w.p1.pid); n != 0 {
		t.Errorf("%d comments stored", n)
	}
	// A comment of p1 is invisible from p2.
	w.a.mustJSONAs(w.alice, http.MethodPost, commentsPath(w.p1), map[string]string{"body": "secret"}, http.StatusCreated, nil)
	var page comments.Page
	w.a.mustJSONAs(w.bob, http.MethodGet, commentsPath(w.p2), nil, http.StatusOK, &page)
	if len(page.Items) != 0 {
		t.Errorf("p2 sees %+v", page.Items)
	}
	w.a.mustJSONAs(w.bob, http.MethodGet, commentsPath(w.p1), nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.bob, http.MethodPost, commentsPath(w.p1), map[string]string{"body": "x"}, http.StatusNotFound, nil)
}

func TestDeletingAMessageDeletesItsComments(t *testing.T) {
	w := newWorld(t)
	w.a.mustJSONAs(w.alice, http.MethodPost, commentsPath(w.p1), map[string]string{"body": "x"}, http.StatusCreated, nil)
	if _, err := w.a.pool.Exec(context.Background(), "DELETE FROM messages WHERE id=$1", w.p1.mid); err != nil {
		t.Fatal(err)
	}
	if n := countComments(t, w, w.p1.pid); n != 0 {
		t.Errorf("%d comments remain", n)
	}
}

func TestCommentPublishesAnEvent(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.a.srv.URL+pp(w.p1.pid, "/events"), nil)
	req.Header.Set("Authorization", "Bearer "+w.vera)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	next := func(prefix string) string {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), prefix) {
				return sc.Text()
			}
		}
		t.Fatalf("stream ended before %q: %v", prefix, sc.Err())
		return ""
	}
	next("event: ready")
	w.a.mustJSONAs(w.alice, http.MethodPost, commentsPath(w.p1), map[string]string{"body": "ping"}, http.StatusCreated, nil)
	if got := next("event: comment.created"); got == "" {
		t.Fatal("no event")
	}
	if data := next("data:"); !strings.Contains(data, `"message_id":`) {
		t.Errorf("payload %s", data)
	}
}

func TestActivityFeed(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	path := pp(w.p1.pid, "/activity")
	w.a.mustJSONAs(w.alice, http.MethodPost, commentsPath(w.p1), map[string]string{"body": strings.Repeat("x", 300)}, http.StatusCreated, nil)
	// An owner only change and a release (as the release endpoint audits it) in p1,
	// and a release in p2 that must never show up in p1.
	w.a.mustJSONAs(w.owner1, http.MethodPut, pp(w.p1.pid, "/policy"), map[string]any{"mode": "capture"}, http.StatusOK, nil)
	for _, r := range []struct {
		pid int64
		mid int64
	}{{w.p1.pid, w.p1.mid}, {w.p2.pid, w.p2.mid}, {w.p1.pid, w.p2.mid}} {
		if _, err := w.a.pool.Exec(ctx, `INSERT INTO audit_log (project_id, action, target, detail)
			VALUES ($1, 'message.release', 'message:' || $2::bigint::text, '{"targets":["qa@corp.example"]}')`, r.pid, r.mid); err != nil {
			t.Fatal(err)
		}
	}

	var page activity.Page
	w.a.mustJSONAs(w.vera, http.MethodGet, path, nil, http.StatusOK, &page)
	kinds := map[string]int{}
	for _, it := range page.Items {
		kinds[it.Kind]++
		switch it.Kind {
		case activity.KindComment:
			if len(it.Detail) != 200 || it.Actor != "alice@example.test" || it.MessageID != w.p1.mid {
				t.Errorf("comment item %+v", it)
			}
		case activity.KindRelease:
			if it.Detail != "qa@corp.example" {
				t.Errorf("release item %+v", it)
			}
		}
	}
	// One message, one comment, and the two releases of p1 (the one targeting a
	// foreign message id has no subject but must not leak the other project's).
	if kinds[activity.KindMessage] != 1 || kinds[activity.KindComment] != 1 || kinds[activity.KindRelease] != 2 || len(page.Items) != 4 {
		t.Fatalf("items %+v", page.Items)
	}
	for _, it := range page.Items {
		if it.Kind == activity.KindRelease && it.MessageID != w.p1.mid && (it.MessageID != 0 || it.Subject != "") {
			t.Errorf("release leaks a foreign message: %+v", it)
		}
	}
	// Nothing owner only: no policy, transport or credential rows, whatever the role.
	_, raw := w.a.doAs(w.vera, http.MethodGet, path, nil)
	for _, banned := range []string{"policy", "transport", "credential", "capture"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("feed mentions %q: %s", banned, raw)
		}
	}

	// Paging walks every item once, in order, without repeats.
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		var p activity.Page
		w.a.mustJSONAs(w.vera, http.MethodGet, path+"?limit=1&cursor="+cursor, nil, http.StatusOK, &p)
		for _, it := range p.Items {
			key := it.Kind + ":" + itoa64(it.ID)
			if seen[key] {
				t.Fatalf("repeated %s", key)
			}
			seen[key] = true
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 4 {
		t.Errorf("paging saw %d items", len(seen))
	}
	w.a.mustJSONAs(w.vera, http.MethodGet, path+"?cursor=bad", nil, http.StatusBadRequest, nil)

	// p2 sees none of p1's activity and nobody else sees p1's.
	var other activity.Page
	w.a.mustJSONAs(w.bob, http.MethodGet, pp(w.p2.pid, "/activity"), nil, http.StatusOK, &other)
	for _, it := range other.Items {
		if it.Kind == activity.KindComment {
			t.Errorf("p2 sees a comment: %+v", it)
		}
	}
	w.a.mustJSONAs(w.bob, http.MethodGet, path, nil, http.StatusNotFound, nil)
	w.a.mustJSONAs(w.proj1Read, http.MethodGet, path, nil, http.StatusOK, nil)
}
