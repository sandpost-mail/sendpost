package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sandpost-mail/sandpost/internal/api"
)

// The OpenAPI document is written by hand and kept honest by this test, which
// runs in CI: it must be well formed, and it must describe exactly the routes
// the server registers, with the same capabilities and the same auth rules.

type doc = map[string]any

func loadSpec(t *testing.T) doc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}
	return d
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

var httpMethods = []string{"get", "post", "put", "patch", "delete"}

// operations returns "METHOD /path" mapped to the operation object.
func operations(t *testing.T, d doc) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for path, item := range asMap(d["paths"]) {
		for _, m := range httpMethods {
			if op := asMap(asMap(item)[m]); op != nil {
				out[strings.ToUpper(m)+" "+path] = op
			}
		}
	}
	return out
}

func TestOpenAPIIsWellFormed(t *testing.T) {
	d := loadSpec(t)
	if v, _ := d["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Errorf("openapi version %q", v)
	}
	if asMap(d["info"])["title"] == nil || asMap(d["info"])["version"] == nil {
		t.Error("info.title and info.version are required")
	}

	// Every $ref resolves.
	refs := map[string]bool{}
	collectRefs(d, refs)
	for ref := range refs {
		if !resolves(d, ref) {
			t.Errorf("unresolved reference %s", ref)
		}
	}

	ids := map[string]string{}
	pathParam := regexp.MustCompile(`\{(\w+)\}`)
	for key, op := range operations(t, d) {
		path := strings.SplitN(key, " ", 2)[1]
		id, _ := op["operationId"].(string)
		if id == "" {
			t.Errorf("%s: operationId is required", key)
		} else if prev, dup := ids[id]; dup {
			t.Errorf("%s: operationId %q is also used by %s", key, id, prev)
		}
		ids[id] = key
		if len(asMap(op["responses"])) == 0 {
			t.Errorf("%s: no responses", key)
		}
		if tags, _ := op["tags"].([]any); len(tags) == 0 {
			t.Errorf("%s: tags are required", key)
		}

		declared := declaredPathParams(d, path, op)
		for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
			if !declared[m[1]] {
				t.Errorf("%s: path parameter {%s} is not declared", key, m[1])
			}
			delete(declared, m[1])
		}
		for extra := range declared {
			t.Errorf("%s: declared path parameter %q is not in the path", key, extra)
		}
	}
}

func collectRefs(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "$ref" {
				out[s] = true
			}
			collectRefs(val, out)
		}
	case []any:
		for _, val := range x {
			collectRefs(val, out)
		}
	}
}

func resolves(d doc, ref string) bool {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var cur any = d
	for _, part := range strings.Split(path, "/") {
		m := asMap(cur)
		if m == nil {
			return false
		}
		if cur, ok = m[part]; !ok {
			return false
		}
	}
	return true
}

func declaredPathParams(d doc, path string, op map[string]any) map[string]bool {
	out := map[string]bool{}
	add := func(list any) {
		items, _ := list.([]any)
		for _, it := range items {
			p := asMap(it)
			if ref, ok := p["$ref"].(string); ok {
				p = asMap(lookup(d, ref))
			}
			if p["in"] == "path" {
				name, _ := p["name"].(string)
				out[name] = true
			}
		}
	}
	add(asMap(asMap(d["paths"])[path])["parameters"])
	add(op["parameters"])
	return out
}

func lookup(d doc, ref string) any {
	var cur any = d
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		cur = asMap(cur)[part]
	}
	return cur
}

func TestOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	d := loadSpec(t)
	ops := operations(t, d)

	routes := map[string]api.RouteInfo{}
	for _, r := range api.Routes() {
		routes[r.Method+" "+r.Path] = r
	}
	var missing, extra []string
	for key := range routes {
		if _, ok := ops[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range ops {
		if _, ok := routes[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("routes missing from docs/openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("documented but not registered:\n  %s", strings.Join(extra, "\n  "))
	}

	for key, r := range routes {
		op, ok := ops[key]
		if !ok {
			continue
		}
		t.Run(key, func(t *testing.T) { checkRoute(t, r, op) })
	}
}

func checkRoute(t *testing.T, r api.RouteInfo, op map[string]any) {
	t.Helper()
	responses := asMap(op["responses"])
	has := func(code string) bool { _, ok := responses[code]; return ok }

	capability, _ := op["x-required-capability"].(string)
	if capability != r.Capability {
		t.Errorf("x-required-capability = %q, the server requires %q", capability, r.Capability)
	}
	signed, _ := op["x-signed-grant"].(bool)
	if signed != (r.Access == "signed") {
		t.Errorf("x-signed-grant = %v for access %q", signed, r.Access)
	}

	secOverride, hasSecurity := op["security"].([]any)
	switch r.Access {
	case "public":
		if !hasSecurity || len(secOverride) != 0 {
			t.Error(`public routes must declare "security: []"`)
		}
		if has("401") && r.Path != "/auth/login" {
			t.Error("a public route documents 401")
		}
	case "signed":
		if !hasSecurity || !hasAnonymousOption(secOverride) {
			t.Error(`signed routes must allow the anonymous option ({}) in security`)
		}
	default:
		if hasSecurity {
			t.Errorf("%s routes must use the document wide security", r.Access)
		}
	}

	if r.Access != "public" && !has("401") {
		t.Error("401 is not documented")
	}
	if (r.Access == "global" || r.Access == "project" || r.Access == "signed" || r.Access == "user") && !has("403") {
		t.Error("403 is not documented")
	}
	if (r.Access == "project" || r.Access == "signed") && !has("404") {
		t.Error("404 is not documented")
	}
	if strings.Contains(r.Path, "{pid}") && !has("400") {
		t.Error("400 (invalid id) is not documented")
	}
	if !has("200") && !has("201") && !has("202") && !has("204") {
		t.Error("no success response documented")
	}
	if (r.Method == "POST" || r.Method == "PATCH") && takesBody(r) && op["requestBody"] == nil {
		t.Error("request body is not documented")
	}
}

func hasAnonymousOption(sec []any) bool {
	for _, s := range sec {
		if m := asMap(s); m != nil && len(m) == 0 {
			return true
		}
	}
	return false
}

// takesBody is false for the POST routes that take no body at all.
func takesBody(r api.RouteInfo) bool {
	switch fmt.Sprintf("%s %s", r.Method, r.Path) {
	case "POST /auth/logout", "POST /projects/{pid}/messages/{mid}/read", "POST /projects/{pid}/transport/test":
		return false
	}
	return true
}
