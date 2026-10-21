package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The compatibility tests check response bodies against the vendored Mailpit
// swagger (docs/mailpit/swagger-v1.31.6.json). The swagger lists properties
// but no "required", so the check is: every declared property is present with
// the declared type, nothing undeclared is present (apart from a few legacy
// fields), and a list is never null.

const swaggerFile = "swagger-v1.31.6.json"

type swagger struct {
	Paths       map[string]map[string]swaggerOp `json:"paths"`
	Definitions map[string]map[string]any       `json:"definitions"`
	Responses   map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"responses"`
}

type swaggerOp struct {
	OperationID string                    `json:"operationId"`
	Responses   map[string]map[string]any `json:"responses"`
}

func loadSwagger(t *testing.T) *swagger {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mailpit", swaggerFile))
	if err != nil {
		t.Fatal(err)
	}
	var sw swagger
	if err := json.Unmarshal(raw, &sw); err != nil {
		t.Fatalf("%s is not valid JSON: %v", swaggerFile, err)
	}
	return &sw
}

// legacyFields are sent by Mailpit but missing from its swagger.
var legacyFields = map[string]bool{"count": true}

// validate checks v (decoded JSON) against schema and returns the problems.
func (sw *swagger) validate(path string, v any, schema map[string]any) []string {
	if ref, ok := schema["$ref"].(string); ok {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if strings.HasPrefix(ref, "#/responses/") {
			return sw.validate(path, v, sw.Responses[name].Schema)
		}
		def, ok := sw.Definitions[name]
		if !ok {
			return []string{fmt.Sprintf("%s: unknown reference %s", path, ref)}
		}
		return sw.validate(path, v, def)
	}
	switch schema["type"] {
	case "object":
		return sw.validateObject(path, v, schema)
	case "array":
		list, ok := v.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want array, got %T", path, v)}
		}
		items, _ := schema["items"].(map[string]any)
		var out []string
		for i, it := range list {
			out = append(out, sw.validate(fmt.Sprintf("%s[%d]", path, i), it, items)...)
		}
		return out
	case "string":
		if _, ok := v.(string); !ok {
			return []string{fmt.Sprintf("%s: want string, got %T", path, v)}
		}
	case "integer", "number":
		if f, ok := v.(float64); !ok || (schema["type"] == "integer" && f != float64(int64(f))) {
			return []string{fmt.Sprintf("%s: want %v, got %v", path, schema["type"], v)}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return []string{fmt.Sprintf("%s: want boolean, got %T", path, v)}
		}
	}
	return nil
}

func (sw *swagger) validateObject(path string, v any, schema map[string]any) []string {
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: want object, got %T", path, v)}
	}
	props, _ := schema["properties"].(map[string]any)
	extra, _ := schema["additionalProperties"].(map[string]any)
	var out []string
	if props == nil && extra != nil { // a map
		for k, val := range obj {
			out = append(out, sw.validate(path+"."+k, val, extra)...)
		}
		return out
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		val, present := obj[name]
		if !present {
			out = append(out, fmt.Sprintf("%s.%s: missing", path, name))
			continue
		}
		prop, _ := props[name].(map[string]any)
		out = append(out, sw.validate(path+"."+name, val, prop)...)
	}
	if props != nil {
		for k := range obj {
			if _, ok := props[k]; !ok && !legacyFields[k] {
				out = append(out, fmt.Sprintf("%s.%s: not in the contract", path, k))
			}
		}
	}
	return out
}

// schemaOf returns the schema of an operation's response, or nil when the
// response has none we check (plain text answers).
func (sw *swagger) schemaOf(method, path, status string) map[string]any {
	resp := sw.Paths[path][strings.ToLower(method)].Responses[status]
	if schema, ok := resp["schema"].(map[string]any); ok {
		return schema
	}
	ref, _ := resp["$ref"].(string)
	name := ref[strings.LastIndex(ref, "/")+1:]
	switch {
	case strings.HasPrefix(ref, "#/responses/"):
		return sw.Responses[name].Schema
	case strings.HasPrefix(ref, "#/definitions/"):
		return map[string]any{"$ref": ref}
	}
	return nil
}

func TestSchemaValidatorCatchesDrift(t *testing.T) {
	sw := loadSwagger(t)
	msg := sw.Definitions["MessageSummary"]
	tests := []struct {
		name string
		body string
		want string // substring of a problem, "" for none
	}{
		{"complete", `{"ID":"1","MessageID":"m","Read":false,"From":{"Name":"","Address":"a"},"To":[],"Cc":[],"Bcc":[],"ReplyTo":[],"Subject":"s","Created":"2026-01-01T00:00:00Z","Username":"","Tags":[],"Size":1,"Attachments":0,"Snippet":""}`, ""},
		{"missing field", `{"ID":"1"}`, ".MessageID: missing"},
		{"null list", `{"ID":"1","MessageID":"m","Read":false,"From":null,"To":null,"Cc":[],"Bcc":[],"ReplyTo":[],"Subject":"s","Created":"x","Username":"","Tags":[],"Size":1,"Attachments":0,"Snippet":""}`, ".To: want array"},
		{"wrong type", `{"ID":1,"MessageID":"m","Read":false,"From":{"Name":"","Address":""},"To":[],"Cc":[],"Bcc":[],"ReplyTo":[],"Subject":"s","Created":"x","Username":"","Tags":[],"Size":1,"Attachments":0,"Snippet":""}`, ".ID: want string"},
		{"extra field", `{"ID":"1","MessageID":"m","Read":false,"From":{"Name":"","Address":""},"To":[],"Cc":[],"Bcc":[],"ReplyTo":[],"Subject":"s","Created":"x","Username":"","Tags":[],"Size":1,"Attachments":0,"Snippet":"","Extra":1}`, ".Extra: not in the contract"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tc.body), &v); err != nil {
				t.Fatal(err)
			}
			problems := strings.Join(sw.validate("$", v, msg), "\n")
			if tc.want == "" && problems != "" || !strings.Contains(problems, tc.want) {
				t.Errorf("problems:\n%s\nwant substring %q", problems, tc.want)
			}
		})
	}
}
