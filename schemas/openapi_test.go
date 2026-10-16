package schemas

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOpenAPIResponseContractsAreCompleteAndResolvable(t *testing.T) {
	b, err := Files.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"Result", "Source", "Occurrence", "Usage", "Attempt", "Citation", "RetrievalEvent", "EvidenceQuery", "Run", "Plan", "Error"} {
		if _, ok := components[name]; !ok {
			t.Errorf("missing public schema %q", name)
		}
	}
	paths := doc["paths"].(map[string]any)
	want := map[string]string{
		"/v1/plans": "Plan", "/v1/runs": "Run", "/v1/runs/{id}": "Run",
		"/v1/runs/{id}/result": "Result", "/v1/runs/{id}/cancel": "Run",
		"/v1/artifacts/{id}": "Result",
	}
	for path, schema := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("missing path %s", path)
			continue
		}
		for _, method := range []string{"get", "post"} {
			op, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			responses := op["responses"].(map[string]any)
			status := "200"
			if path == "/v1/runs" {
				status = "202"
			}
			if path == "/v1/runs/{id}/result" && method == "get" {
				status = "200"
			}
			r, ok := responses[status].(map[string]any)
			if !ok {
				t.Errorf("%s %s missing %s response", method, path, status)
				continue
			}
			content, _ := r["content"].(map[string]any)
			if content == nil {
				t.Errorf("%s %s response has no content", method, path)
				continue
			}
			media := "application/json"
			m, _ := content[media].(map[string]any)
			if m == nil {
				t.Errorf("%s %s missing JSON response", method, path)
				continue
			}
			ref, _ := m["schema"].(map[string]any)["$ref"].(string)
			if !strings.HasSuffix(ref, "/"+schema) {
				t.Errorf("%s %s response ref = %q, want %s", method, path, ref, schema)
			}
			break
		}
	}
	stream := paths["/v1/runs/{id}/events"].(map[string]any)["get"].(map[string]any)
	if !strings.Contains(stream["description"].(string), "Last-Event-ID") || !strings.Contains(stream["description"].(string), "after") {
		t.Error("SSE contract must document Last-Event-ID and after cursor")
	}
	var walk func(any, string)
	walk = func(v any, at string) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if _, exists := components[name]; !exists {
					t.Errorf("unresolved schema ref %q at %s", ref, at)
				}
			}
			for k, child := range x {
				walk(child, fmt.Sprintf("%s/%s", at, k))
			}
		case []any:
			for i, child := range x {
				walk(child, fmt.Sprintf("%s/%d", at, i))
			}
		}
	}
	walk(doc, "openapi")
}
