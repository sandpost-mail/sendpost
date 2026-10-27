package api_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
)

// The compatible API is checked against two documents: the vendored Mailpit
// swagger (what Mailpit offers) and docs/MAILPIT_COMPATIBILITY.md (what
// Sandpost says about each endpoint). Neither can drift from the route table.

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func swaggerOperations(sw *swagger) map[string]bool {
	ops := map[string]bool{}
	for path, item := range sw.Paths {
		for method := range item {
			ops[strings.ToUpper(method)+" "+path] = true
		}
	}
	return ops
}

func TestCompatRoutesCoverTheMailpitContract(t *testing.T) {
	sw := loadSwagger(t)
	want := swaggerOperations(sw)
	got := map[string]bool{}
	for _, rt := range api.CompatRoutes() {
		key := rt.Method + " " + rt.Path
		if got[key] {
			t.Errorf("route %s is declared twice", key)
		}
		got[key] = true
	}
	for _, k := range sortedKeys(want) {
		if !got[k] {
			t.Errorf("in the Mailpit contract but not in the route table (add it, or mark it unsupported): %s", k)
		}
	}
	for _, k := range sortedKeys(got) {
		if !want[k] {
			t.Errorf("in the route table but not in the Mailpit contract: %s", k)
		}
	}
}

// matrixRow is one endpoint line of docs/MAILPIT_COMPATIBILITY.md.
type matrixRow struct{ status, notes string }

func loadMatrix(t *testing.T) map[string]matrixRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "MAILPIT_COMPATIBILITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]matrixRow{}
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 3 {
			continue
		}
		method := strings.TrimSpace(cells[0])
		path := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if !strings.HasPrefix(path, "/") || strings.ToUpper(method) != method {
			continue
		}
		key := method + " " + path
		if _, dup := rows[key]; dup {
			t.Errorf("the matrix lists %s twice", key)
		}
		row := matrixRow{status: strings.TrimSpace(cells[2])}
		if len(cells) > 3 {
			row.notes = strings.TrimSpace(cells[3])
		}
		rows[key] = row
	}
	return rows
}

func TestCompatMatrixMatchesRoutes(t *testing.T) {
	matrix := loadMatrix(t)
	valid := map[string]bool{"implemented": true, "partial": true, "unsupported": true}
	seen := map[string]bool{}
	for _, rt := range api.CompatRoutes() {
		key := rt.Method + " " + rt.Path
		seen[key] = true
		row, ok := matrix[key]
		if !ok {
			t.Errorf("docs/MAILPIT_COMPATIBILITY.md has no row for %s", key)
			continue
		}
		if !valid[row.status] {
			t.Errorf("%s: status %q is not implemented, partial or unsupported", key, row.status)
		}
		if rt.Supported == (row.status == "unsupported") {
			t.Errorf("%s: the route is supported=%v but the matrix says %q", key, rt.Supported, row.status)
		}
		if row.status != "implemented" && row.notes == "" {
			t.Errorf("%s: a %s endpoint needs a note saying what differs", key, row.status)
		}
	}
	for _, k := range sortedKeys(matrix) {
		if !seen[k] {
			t.Errorf("the matrix lists %s, which is not a route", k)
		}
	}
}

func TestCompatCapabilities(t *testing.T) {
	writes := map[string]bool{
		"DELETE /api/v1/messages": true, "DELETE /api/v1/search": true, "PUT /api/v1/tags": true,
		"PUT /api/v1/tags/{Tag}": true, "DELETE /api/v1/tags/{Tag}": true, "POST /api/v1/message/{ID}/release": true,
	}
	for _, rt := range api.CompatRoutes() {
		want := "messages.read"
		if writes[rt.Method+" "+rt.Path] {
			want = "messages.write"
		}
		if rt.Capability != want {
			t.Errorf("%s %s needs %q, table says %q", rt.Method, rt.Path, want, rt.Capability)
		}
	}
}
