package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
)

// compatApp is a world whose project "one" holds four messages, oldest first:
// attachments_inline (w.p1.mid), simple_text, html_only, multipart_alternative.
type compatApp struct {
	*world
	token                 string // project token of project one, scope write
	simple, htmlOnly, alt int64
	sw                    *swagger
}

func newCompatApp(t *testing.T) *compatApp {
	t.Helper()
	w := newWorld(t)
	c := &compatApp{world: w, token: w.proj1Write, sw: loadSwagger(t)}
	// The world's tag rule tags every message "t", which would show up in the
	// tag assertions. Remove the rule, and the tag it put on the first message.
	w.a.mustJSON(http.MethodDelete, pp(w.p1.pid, "/tag-rules/%d", w.p1.rid), nil, http.StatusNoContent, nil)
	w.a.mustMP(c.token, http.MethodDelete, "/api/v1/tags/t", "", http.StatusOK)
	c.simple = w.a.capture(w.p1.pid, "simple_text.eml")
	c.htmlOnly = w.a.capture(w.p1.pid, "html_only.eml")
	c.alt = w.a.capture(w.p1.pid, "multipart_alternative.eml")
	return c
}

func (c *compatApp) get(path string) compatResp {
	c.a.t.Helper()
	return c.a.mustMP(c.token, http.MethodGet, path, "", http.StatusOK)
}

type listBody struct {
	Total          int64    `json:"total"`
	Unread         int64    `json:"unread"`
	Count          int64    `json:"count"`
	MessagesCount  int64    `json:"messages_count"`
	MessagesUnread int64    `json:"messages_unread"`
	Start          int64    `json:"start"`
	Tags           []string `json:"tags"`
	Messages       []struct {
		ID, Subject, Snippet string
		Read                 bool
		Attachments          int
		Tags                 []string
		From                 *struct{ Name, Address string }
	} `json:"messages"`
}

func (c *compatApp) list(path string) listBody {
	c.a.t.Helper()
	var l listBody
	c.get(path).decode(c.a.t, &l)
	return l
}

func subjects(l listBody) []string {
	var out []string
	for _, m := range l.Messages {
		out = append(out, m.Subject)
	}
	return out
}

func TestCompatList(t *testing.T) {
	c := newCompatApp(t)
	r := c.get("/api/v1/messages")
	checkShape(t, c.sw, r, "GET", "/api/v1/messages")
	if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type %q", got)
	}

	l := c.list("/api/v1/messages")
	want := []string{"Your order", "HTML only", "Plain hello", "Report with files"}
	if strings.Join(subjects(l), "|") != strings.Join(want, "|") {
		t.Errorf("order: %v", subjects(l))
	}
	if l.Total != 4 || l.Unread != 4 || l.MessagesCount != 4 || l.MessagesUnread != 4 || l.Count != 4 || l.Start != 0 {
		t.Errorf("counters: %+v", l)
	}

	tests := []struct {
		name, query string
		wantSubj    []string
		wantStart   int64
		wantCount   int64
	}{
		{"limit", "?limit=2", []string{"Your order", "HTML only"}, 0, 4},
		{"start and limit", "?start=1&limit=2", []string{"HTML only", "Plain hello"}, 1, 4},
		{"start past the end", "?start=10", nil, 10, 4},
		{"limit zero means all", "?limit=0", want, 0, 4},
		{"junk falls back to defaults", "?start=x&limit=y", want, 0, 4},
		{"negative start is zero", "?start=-3", want, 0, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := c.list("/api/v1/messages" + tc.query)
			if strings.Join(subjects(l), "|") != strings.Join(tc.wantSubj, "|") || l.Start != tc.wantStart || l.MessagesCount != tc.wantCount {
				t.Errorf("subjects %v start %d count %d", subjects(l), l.Start, l.MessagesCount)
			}
		})
	}
}

func TestCompatSummaryContent(t *testing.T) {
	c := newCompatApp(t)
	byID := map[string]int{}
	l := c.list("/api/v1/messages")
	for i, m := range l.Messages {
		byID[m.ID] = i
	}
	simple := l.Messages[byID[itoa64(c.simple)]]
	if simple.From == nil || simple.From.Name != "Alice Example" || simple.From.Address != "alice@example.test" {
		t.Errorf("from: %+v", simple.From)
	}
	if !strings.HasPrefix(simple.Snippet, "Hello Bob, Visit https://example.test/welcome") {
		t.Errorf("snippet: %q", simple.Snippet)
	}
	if got := l.Messages[byID[itoa64(c.htmlOnly)]].Snippet; !strings.Contains(got, "Headline") || strings.Contains(got, "<") {
		t.Errorf("html only snippet: %q", got)
	}
	if got := l.Messages[byID[itoa64(c.p1.mid)]].Attachments; got != 1 {
		t.Errorf("attachments counts files only, got %d", got)
	}
	var raw struct {
		Messages []struct {
			ID  string
			To  []struct{ Name, Address string }
			Cc  []struct{ Name, Address string }
			Bcc []struct{ Name, Address string }
		}
	}
	c.get("/api/v1/messages").decode(t, &raw)
	for _, m := range raw.Messages {
		if m.ID != itoa64(c.simple) {
			continue
		}
		if len(m.To) != 2 || m.To[0].Name != "Bob" || m.To[0].Address != "bob@example.test" || m.To[1].Address != "carol@example.test" {
			t.Errorf("to: %+v", m.To)
		}
		if len(m.Cc) != 1 || m.Cc[0].Address != "dave@example.test" {
			t.Errorf("cc: %+v", m.Cc)
		}
		// The envelope recipient (x@example.test) is in neither To nor Cc: that is Bcc.
		if len(m.Bcc) != 1 || m.Bcc[0].Address != "x@example.test" {
			t.Errorf("bcc: %+v", m.Bcc)
		}
	}
}

func TestCompatSearch(t *testing.T) {
	c := newCompatApp(t)
	q := func(s string) string { return "/api/v1/search?query=" + url.QueryEscape(s) }
	tests := []struct {
		name, path string
		want       []string
	}{
		{"from", q("from:alice"), []string{"Plain hello"}},
		{"subject phrase", q(`subject:"Plain hello"`), []string{"Plain hello"}},
		{"free text", q("confirm"), []string{"Your order"}},
		{"free text in html only mail", q("Headline"), []string{"HTML only"}},
		{"negation", q("-subject:order -subject:report"), []string{"HTML only", "Plain hello"}},
		{"to and cc", q("to:bob cc:dave"), []string{"Plain hello"}},
		{"unread", q("is:unread"), []string{"Your order", "HTML only", "Plain hello", "Report with files"}},
		{"attachments", q("has:attachment"), []string{"Report with files"}},
		{"after", q("after:2000-01-01"), []string{"Your order", "HTML only", "Plain hello", "Report with files"}},
		{"before in another zone", q("before:2000-01-01") + "&tz=Europe/Paris", nil},
		{"paged", q("is:unread") + "&limit=1&start=1", []string{"HTML only"}},
		{"nothing", q("from:nobody"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := c.list(tc.path)
			if strings.Join(subjects(l), "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %v want %v", subjects(l), tc.want)
			}
			if l.Total != 4 {
				t.Errorf("total is the whole mailbox: %d", l.Total)
			}
		})
	}
	r := c.get(q("from:alice"))
	checkShape(t, c.sw, r, "GET", "/api/v1/search")
	if l := c.list(q("is:unread")); l.MessagesCount != 4 || l.Count != 4 {
		t.Errorf("messages_count counts matches, not the page: %+v", l)
	}
	if l := c.list(q("is:unread") + "&limit=1"); l.MessagesCount != 4 || l.Count != 1 {
		t.Errorf("paged counters: %+v", l)
	}

	for _, bad := range []string{"/api/v1/search", "/api/v1/search?query=", "/api/v1/search?query=after:never"} {
		if r := c.a.mp(c.token, "GET", bad, ""); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", bad, r.StatusCode, r.body)
		}
	}
}

func TestCompatGetMessage(t *testing.T) {
	c := newCompatApp(t)
	id := itoa64(c.simple)
	r := c.get("/api/v1/message/" + id)
	checkShape(t, c.sw, r, "GET", "/api/v1/message/{ID}")
	var m struct {
		ID, MessageID, Subject, Text, ReturnPath string
		From                                     struct{ Name, Address string }
		Date                                     string
		Size                                     int
		Inline, Attachments                      []any
		ListUnsubscribe                          struct{ Links []string }
	}
	r.decode(t, &m)
	if m.ID != id || m.MessageID != "simple-1@example.test" || m.Subject != "Plain hello" {
		t.Errorf("message: %+v", m)
	}
	if m.From.Name != "Alice Example" || m.ReturnPath != "alice@example.test" {
		t.Errorf("from %+v return path %q", m.From, m.ReturnPath)
	}
	if !strings.HasPrefix(m.Date, "2026-10-14T10:00:00") || !strings.HasPrefix(m.Text, "Hello Bob,") || m.Size == 0 {
		t.Errorf("date %q text %q size %d", m.Date, m.Text, m.Size)
	}
	if len(m.Inline) != 0 || len(m.Attachments) != 0 || m.ListUnsubscribe.Links == nil {
		t.Errorf("lists must be present and empty: %+v", m)
	}
	// Like Mailpit, fetching a message marks it read.
	if l := c.list("/api/v1/search?query=is:read"); len(l.Messages) != 1 || l.Messages[0].ID != id {
		t.Errorf("read after get: %+v", l.Messages)
	}

	withFiles := c.get("/api/v1/message/" + itoa64(c.p1.mid))
	checkShape(t, c.sw, withFiles, "GET", "/api/v1/message/{ID}")
	var f struct {
		HTML                string
		Inline, Attachments []struct {
			PartID, FileName, ContentType, ContentID string
			Size                                     int
			Checksums                                struct{ MD5, SHA1, SHA256 string }
		}
	}
	withFiles.decode(t, &f)
	if len(f.Attachments) != 1 || f.Attachments[0].FileName != "report.pdf" || f.Attachments[0].PartID != "0" ||
		f.Attachments[0].ContentType != "application/pdf" || len(f.Attachments[0].Checksums.SHA256) != 64 {
		t.Errorf("attachments: %+v", f.Attachments)
	}
	if len(f.Inline) != 1 || f.Inline[0].FileName != "logo.png" || f.Inline[0].ContentID != "logo123" || f.Inline[0].PartID != "1" {
		t.Errorf("inline: %+v", f.Inline)
	}
	if !strings.Contains(f.HTML, "cid:logo123") {
		t.Errorf("html: %q", f.HTML)
	}
}

func TestCompatLatestAndBadIDs(t *testing.T) {
	c := newCompatApp(t)
	var m struct{ ID string }
	c.get("/api/v1/message/latest").decode(t, &m)
	if m.ID != itoa64(c.alt) {
		t.Errorf("latest = %s want %d", m.ID, c.alt)
	}
	m = struct{ ID string }{}
	c.get("/api/v1/message/latest?query="+url.QueryEscape("from:alice")).decode(t, &m)
	if m.ID != itoa64(c.simple) {
		t.Errorf("latest of a search = %s want %d", m.ID, c.simple)
	}
	for _, path := range []string{
		"/api/v1/message/999999", "/api/v1/message/abc", "/api/v1/message/0", "/api/v1/message/-1",
		"/api/v1/message/latest?query=" + url.QueryEscape("from:nobody"),
		"/api/v1/message/999999/raw", "/api/v1/message/999999/headers", "/api/v1/message/999999/part/0",
		"/view/999999.html", "/view/abc.txt", "/view/nothing",
	} {
		r := c.a.mp(c.token, "GET", path, "")
		if r.StatusCode != http.StatusNotFound || r.text() != "404 page not found" {
			t.Errorf("%s: %d %q", path, r.StatusCode, r.body)
		}
	}
}

func TestCompatHeadersRawAndParts(t *testing.T) {
	c := newCompatApp(t)
	id := itoa64(c.simple)

	r := c.get("/api/v1/message/" + id + "/headers")
	checkShape(t, c.sw, r, "GET", "/api/v1/message/{ID}/headers")
	var h map[string][]string
	r.decode(t, &h)
	if h["Subject"][0] != "Plain hello" || h["X-Run-Id"][0] != "run-42" {
		t.Errorf("headers: %v", h)
	}

	raw := c.get("/api/v1/message/" + id + "/raw")
	if !strings.HasPrefix(raw.Header.Get("Content-Type"), "text/plain") || !strings.Contains(raw.text(), "Subject: Plain hello") {
		t.Errorf("raw: %q %q", raw.Header.Get("Content-Type"), raw.body)
	}
	if raw.Header.Get("Content-Disposition") != "" {
		t.Error("raw is inline by default")
	}
	dl := c.get("/api/v1/message/" + id + "/raw?dl=1")
	if got := dl.Header.Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, id+".eml") {
		t.Errorf("download: %q", got)
	}

	files := itoa64(c.p1.mid)
	pdf := c.get("/api/v1/message/" + files + "/part/0")
	if pdf.Header.Get("Content-Type") != "application/pdf" || pdf.text() != "%PDF-1.4 fake pdf bytes" {
		t.Errorf("pdf: %q %q", pdf.Header.Get("Content-Type"), pdf.body)
	}
	if got := pdf.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline") || !strings.Contains(got, "report.pdf") {
		t.Errorf("pdf disposition: %q", got)
	}
	png := c.get("/api/v1/message/" + files + "/part/1")
	if png.Header.Get("Content-Type") != "image/png" || len(png.body) == 0 || png.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("png: %q %d bytes", png.Header.Get("Content-Type"), len(png.body))
	}
	for _, p := range []string{"/part/2", "/part/-1", "/part/x"} {
		if r := c.a.mp(c.token, "GET", "/api/v1/message/"+files+p, ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
}

func TestCompatViews(t *testing.T) {
	c := newCompatApp(t)
	files := itoa64(c.p1.mid)
	html := c.get("/view/" + files + ".html")
	if !strings.HasPrefix(html.Header.Get("Content-Type"), "text/html") || !strings.Contains(html.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("html headers: %v", html.Header)
	}
	if !strings.Contains(html.text(), `src="/api/v1/message/`+files+`/part/1"`) || strings.Contains(html.text(), "cid:") {
		t.Errorf("inline image not linked: %s", html.body)
	}
	alias := c.a.mustMP(c.token, "GET", "/p/one/view/"+files+".html", "", http.StatusOK)
	if !strings.Contains(alias.text(), `src="/p/one/api/v1/message/`+files+`/part/1"`) {
		t.Errorf("alias must link through the alias: %s", alias.body)
	}

	txt := c.get("/view/" + itoa64(c.simple) + ".txt")
	if !strings.HasPrefix(txt.Header.Get("Content-Type"), "text/plain") || !strings.HasPrefix(txt.text(), "Hello Bob,") {
		t.Errorf("txt: %q %q", txt.Header.Get("Content-Type"), txt.body)
	}
	latest := c.get("/view/latest.html")
	if !strings.Contains(latest.text(), "Confirm") {
		t.Errorf("latest html: %s", latest.body)
	}
	// A text only mail has no HTML part.
	r := c.a.mp(c.token, "GET", "/view/"+itoa64(c.simple)+".html", "")
	if r.StatusCode != http.StatusNotFound || !strings.Contains(r.text(), "HTML") {
		t.Errorf("no html part: %d %s", r.StatusCode, r.body)
	}
}

func TestCompatSetReadAndDelete(t *testing.T) {
	c := newCompatApp(t)
	put := func(body string) { c.a.t.Helper(); c.a.mustMP(c.token, "PUT", "/api/v1/messages", body, http.StatusOK) }
	unread := func() int64 { return c.list("/api/v1/messages?limit=1").Unread }

	r := c.a.mp(c.token, "PUT", "/api/v1/messages", `{"IDs":["`+itoa64(c.simple)+`"],"Read":true}`)
	if r.StatusCode != 200 || r.text() != "ok" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("answer: %d %q %q", r.StatusCode, r.Header.Get("Content-Type"), r.body)
	}
	if unread() != 3 {
		t.Errorf("unread after read by id: %d", unread())
	}
	put(`{"Search":"from:shop","Read":true}`)
	if unread() != 2 {
		t.Errorf("unread after read by search: %d", unread())
	}
	put(`{"IDs":["` + itoa64(c.simple) + `"],"Read":false}`)
	if unread() != 3 {
		t.Errorf("unread after unread by id: %d", unread())
	}
	put(`{"Read":true}`)
	if unread() != 0 {
		t.Errorf("unread after mark all: %d", unread())
	}
	put(`{"Read":false}`)
	if unread() != 4 {
		t.Errorf("unread after mark all unread: %d", unread())
	}
	for name, body := range map[string]string{
		"ids and search": `{"IDs":["1"],"Search":"from:a","Read":true}`,
		"empty body":     ``,
		"not json":       `nope`,
		"empty search":   `{"Search":"from:","Read":true}`,
		"bad date":       `{"Search":"after:never","Read":true}`,
	} {
		if r := c.a.mp(c.token, "PUT", "/api/v1/messages", body); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", name, r.StatusCode, r.body)
		}
	}
	if unread() != 4 {
		t.Errorf("a refused request changed data: %d", unread())
	}

	// Deleting.
	del := func(method, path, body string, want int) {
		c.a.t.Helper()
		c.a.mustMP(c.token, method, path, body, want)
	}
	total := func() int64 { return c.list("/api/v1/messages?limit=1").Total }
	del("DELETE", "/api/v1/messages", `{"IDs":["`+itoa64(c.htmlOnly)+`","nonsense","999999"]}`, 200)
	if total() != 3 {
		t.Errorf("total after delete by id: %d", total())
	}
	del("DELETE", "/api/v1/search?query="+url.QueryEscape("from:shop"), "", 200)
	if total() != 2 {
		t.Errorf("total after delete by search: %d", total())
	}
	for _, bad := range []string{"/api/v1/search", "/api/v1/search?query=", "/api/v1/search?query=from:"} {
		if r := c.a.mp(c.token, "DELETE", bad, ""); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", bad, r.StatusCode)
		}
	}
	if r := c.a.mp(c.token, "DELETE", "/api/v1/messages", `{not json`); r.StatusCode != http.StatusBadRequest || total() != 2 {
		t.Errorf("malformed body must not delete anything: %d, total %d", r.StatusCode, total())
	}
	del("DELETE", "/api/v1/messages", "", 200) // no body deletes everything, as in Mailpit
	if total() != 0 {
		t.Errorf("total after delete all: %d", total())
	}
	// Project two is untouched.
	if n := c.a.mp(c.a.projectToken(c.p2.pid, "write"), "GET", "/api/v1/messages", ""); n.StatusCode != 200 || !strings.Contains(n.text(), `"total":1`) {
		t.Errorf("another project changed: %s", n.body)
	}
}

func TestCompatTags(t *testing.T) {
	c := newCompatApp(t)
	ids := fmt.Sprintf(`["%d","%d"]`, c.simple, c.alt)
	r := c.a.mustMP(c.token, "PUT", "/api/v1/tags", `{"IDs":`+ids+`,"Tags":["Billing","run 1"]}`, 200)
	if r.text() != "ok" {
		t.Errorf("answer %q", r.body)
	}
	tags := c.get("/api/v1/tags")
	checkShape(t, c.sw, tags, "GET", "/api/v1/tags")
	if tags.text() != "[\"Billing\",\"run 1\"]\n" {
		t.Errorf("tags: %q", tags.body)
	}
	if l := c.list("/api/v1/search?query=" + url.QueryEscape(`tag:"run 1"`)); len(l.Messages) != 2 {
		t.Errorf("search by tag: %v", subjects(l))
	}
	if l := c.list("/api/v1/messages"); strings.Join(l.Tags, ",") != "Billing,run 1" || len(l.Messages[0].Tags) != 2 {
		t.Errorf("list tags: %v / %v", l.Tags, l.Messages[0].Tags)
	}
	var info struct{ Tags map[string]int }
	infoResp := c.get("/api/v1/info")
	checkShape(t, c.sw, infoResp, "GET", "/api/v1/info")
	infoResp.decode(t, &info)
	if info.Tags["Billing"] != 2 {
		t.Errorf("info tags: %v", info.Tags)
	}

	c.a.mustMP(c.token, "PUT", "/api/v1/tags/"+url.PathEscape("run 1"), `{"Name":"run-1"}`, 200)
	if got := c.get("/api/v1/tags").text(); got != "[\"Billing\",\"run-1\"]\n" {
		t.Errorf("after rename: %q", got)
	}
	c.a.mustMP(c.token, "DELETE", "/api/v1/tags/Billing", "", 200)
	if got := c.get("/api/v1/tags").text(); got != "[\"run-1\"]\n" {
		t.Errorf("after delete: %q", got)
	}
	c.a.mustMP(c.token, "PUT", "/api/v1/tags", `{"IDs":`+ids+`,"Tags":[]}`, 200)
	if got := c.get("/api/v1/tags").text(); got != "[]\n" {
		t.Errorf("after clearing: %q", got)
	}

	for name, req := range map[string][3]string{
		"rename missing":      {"PUT", "/api/v1/tags/missing", `{"Name":"x"}`},
		"rename to invalid":   {"PUT", "/api/v1/tags/x", `{"Name":"a/b"}`},
		"delete missing":      {"DELETE", "/api/v1/tags/missing", ""},
		"set with bad body":   {"PUT", "/api/v1/tags", `nope`},
		"rename with no body": {"PUT", "/api/v1/tags/x", ``},
	} {
		if r := c.a.mp(c.token, req[0], req[1], req[2]); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", name, r.StatusCode, r.body)
		}
	}
}

func TestCompatUnsupportedAnswers501(t *testing.T) {
	c := newCompatApp(t)
	for _, rt := range api.CompatRoutes() {
		if rt.Supported {
			continue
		}
		path := strings.NewReplacer("{ID}", "1", "{PartID}", "0").Replace(rt.Path)
		t.Run(rt.Method+" "+rt.Path, func(t *testing.T) {
			r := c.a.mp(c.token, rt.Method, path, "")
			if r.StatusCode != http.StatusNotImplemented || !strings.Contains(r.text(), "MAILPIT_COMPATIBILITY") {
				t.Errorf("status %d: %s", r.StatusCode, r.body)
			}
		})
	}
}

func TestCompatAuthStyles(t *testing.T) {
	c := newCompatApp(t)
	tests := []struct {
		name  string
		style authStyle
		token string
		want  int
	}{
		{"bearer", authBearer, c.token, 200},
		{"basic with any username", authBasic, c.token, 200},
		{"wrong token", authBearer, "spp_nope", 401},
		{"wrong basic password", authBasic, "spp_nope", 401},
		{"no credentials", authNone, "", 401},
		{"empty bearer", authBearer, "", 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := c.a.compat(tc.style, tc.token, "GET", "/api/v1/messages", "")
			if r.StatusCode != tc.want {
				t.Errorf("status %d want %d: %s", r.StatusCode, tc.want, r.body)
			}
			if tc.want == 401 && !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
				t.Errorf("errors are plain text, got %q", r.Header.Get("Content-Type"))
			}
		})
	}
	if r := c.a.compat(authNone, "", "GET", "/api/v1/messages", ""); r.Header.Get("WWW-Authenticate") == "" {
		t.Error("an anonymous request gets a Basic challenge")
	}
	if r := c.a.compat(authBasic, "spp_nope", "GET", "/api/v1/messages", ""); r.Header.Get("WWW-Authenticate") != "" {
		t.Error("wrong credentials must not be challenged again")
	}
	// Another scheme is not understood.
	req, _ := http.NewRequestWithContext(t.Context(), "GET", c.a.srv.URL+"/api/v1/messages", nil)
	req.Header.Set("Authorization", "Token "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("unknown scheme: %d", resp.StatusCode)
	}
}

func TestCompatAliasAndCatchAll(t *testing.T) {
	c := newCompatApp(t)
	// A user token with a role works through the alias, a project token too.
	for name, tok := range map[string]string{"member user": c.alice, "viewer user": c.vera, "project token": c.token} {
		r := c.a.mp(tok, "GET", "/p/one/api/v1/messages", "")
		if r.StatusCode != 200 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
			continue
		}
		checkShape(t, c.sw, r, "GET", "/api/v1/messages")
	}
	if r := c.a.mp(c.alice, "GET", "/api/v1/messages", ""); r.StatusCode != http.StatusForbidden {
		t.Errorf("a user token on the bare path: %d", r.StatusCode)
	}
	// Unknown paths under the prefix answer in Mailpit style, not with the UI.
	for _, p := range []string{"/api/v1/nothing", "/api/v1/", "/p/one/api/v1/nothing", "/api/v1/message"} {
		r := c.a.compat(authNone, "", "GET", p, "")
		if r.StatusCode != http.StatusNotFound || r.text() != "404 page not found" {
			t.Errorf("%s: %d %q", p, r.StatusCode, r.body)
		}
	}
	// The native API and the UI are untouched.
	if r := c.a.compat(authNone, "", "GET", "/", ""); r.text() != "ui" {
		t.Errorf("ui: %q", r.body)
	}
}
