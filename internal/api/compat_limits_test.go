package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// jsonList renders n strings made by item as a JSON array.
func jsonList(n int, item func(i int) string) string {
	out := make([]string, n)
	for i := range out {
		out[i] = item(i)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestCompatSetTagsLimits(t *testing.T) {
	c := newCompatApp(t)
	tag := func(i int) string { return fmt.Sprintf("t%d", i) }
	id := func(i int) string { return fmt.Sprintf("%d", c.simple+int64(i)) }
	tests := []struct {
		name string
		body string
		want int
	}{
		{"100 tags", `{"IDs":` + jsonList(2, id) + `,"Tags":` + jsonList(100, tag) + `}`, http.StatusOK},
		{"101 tags", `{"IDs":` + jsonList(1, id) + `,"Tags":` + jsonList(101, tag) + `}`, http.StatusBadRequest},
		{"ids times tags over the cap", `{"IDs":` + jsonList(10_000, id) + `,"Tags":` + jsonList(11, tag) + `}`, http.StatusBadRequest},
		{"10001 ids", `{"IDs":` + jsonList(10_001, id) + `,"Tags":[]}`, http.StatusBadRequest},
		{"ids times tags at the cap", `{"IDs":` + jsonList(10_000, id) + `,"Tags":` + jsonList(10, tag) + `}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if r := c.a.mp(c.token, http.MethodPut, "/api/v1/tags", tc.body); r.StatusCode != tc.want {
				t.Errorf("status %d want %d: %s", r.StatusCode, tc.want, r.body)
			}
		})
	}
}

// Bodies are decoded strictly: a mistyped key is a 400 and changes nothing,
// in particular it never turns DELETE /messages into "delete everything".
func TestCompatBodiesAreStrict(t *testing.T) {
	c := newCompatApp(t)
	total := func() int64 { return c.list("/api/v1/messages?limit=1").Total }
	unread := func() int64 { return c.list("/api/v1/messages?limit=1").Unread }
	id := fmt.Sprintf(`"%d"`, c.simple)
	bad := []struct {
		name, method, path, body string
	}{
		{"delete with ID", "DELETE", "/api/v1/messages", `{"ID":[` + id + `]}`},
		{"delete with messages", "DELETE", "/api/v1/messages", `{"messages":[` + id + `]}`},
		{"delete with trailing data", "DELETE", "/api/v1/messages", `{"IDs":[]} {"IDs":[]}`},
		{"delete with ids as a string", "DELETE", "/api/v1/messages", `{"IDs":` + id + `}`},
		{"read with a typo", "PUT", "/api/v1/messages", `{"Reed":true}`},
		{"read with a mistyped search", "PUT", "/api/v1/messages", `{"Read":true,"Query":"from:shop"}`},
		{"tags with a typo", "PUT", "/api/v1/tags", `{"IDs":[` + id + `],"Tag":["x"]}`},
		{"rename with a typo", "PUT", "/api/v1/tags/x", `{"NewName":"y"}`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if r := c.a.mp(c.token, tc.method, tc.path, tc.body); r.StatusCode != http.StatusBadRequest {
				t.Errorf("status %d: %s", r.StatusCode, r.body)
			}
			if total() != 4 || unread() != 4 {
				t.Errorf("a refused request changed data: total %d unread %d", total(), unread())
			}
		})
	}

	// Keys match without regard to case, as with Mailpit's decoder.
	c.a.mustMP(c.token, "PUT", "/api/v1/messages", `{"ids":[`+id+`],"read":true}`, http.StatusOK)
	if unread() != 3 {
		t.Errorf("lower case keys: unread %d", unread())
	}
	c.a.mustMP(c.token, "DELETE", "/api/v1/messages", `{"ids":[`+id+`]}`, http.StatusOK)
	if total() != 3 {
		t.Errorf("lower case delete: total %d", total())
	}
	// Mailpit semantics are kept: an empty list deletes everything.
	c.a.mustMP(c.token, "DELETE", "/api/v1/messages", `{"IDs":[]}`, http.StatusOK)
	if total() != 0 {
		t.Errorf("empty list: total %d", total())
	}
	if r := c.a.mp(c.token, "GET", "/api/v1/messages", ""); !strings.Contains(r.text(), `"total":0`) {
		t.Errorf("list after delete all: %s", r.body)
	}
}
