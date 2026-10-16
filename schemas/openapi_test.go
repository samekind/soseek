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

func TestAdminContractUsesCookieSessionAndMutationCSRFHeaders(t *testing.T) {
	b, err := Files.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	adminPaths := []string{
		"/admin/api/session", "/admin/api/overview", "/admin/api/principals",
		"/admin/api/principals/{id}", "/admin/api/principals/{id}/rotate",
		"/admin/api/credentials", "/admin/api/credentials/{id}",
		"/admin/api/credentials/{id}/disable", "/admin/api/runs",
		"/admin/api/runs/{id}", "/admin/api/runs/{id}/cancel", "/admin/api/audit",
	}
	for _, path := range adminPaths {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("missing admin path %s", path)
			continue
		}
		for method, raw := range item {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			security, _ := op["security"].([]any)
			login := path == "/admin/api/session" && method == "post"
			if login {
				if len(security) != 0 {
					t.Errorf("login must exchange a bearer token in its body, not require a cookie: security=%v", security)
				}
			} else if len(security) != 1 || !hasSecurityScheme(security, "cookieAdmin") || hasSecurityScheme(security, "bearerAuth") {
				t.Errorf("%s %s must be cookieAdmin-only, got %v", method, path, security)
			}
			if method == "get" {
				continue
			}
			params, _ := op["parameters"].([]any)
			if !hasRequiredHeader(params, "Origin") || !hasRequiredHeader(params, "X-Soseek-Admin") {
				t.Errorf("%s %s must require Origin and X-Soseek-Admin on mutations", method, path)
			}
		}
	}
	for path, itemRaw := range paths {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		item := itemRaw.(map[string]any)
		for method, opRaw := range item {
			op, ok := opRaw.(map[string]any)
			if !ok {
				continue
			}
			security, _ := op["security"].([]any)
			if len(security) != 1 || !hasSecurityScheme(security, "bearerAuth") || hasSecurityScheme(security, "cookieAdmin") {
				t.Errorf("caller API %s %s must retain bearer-only security, got %v", method, path, security)
			}
		}
	}
	cookie := doc["components"].(map[string]any)["securitySchemes"].(map[string]any)["cookieAdmin"].(map[string]any)
	if cookie["type"] != "apiKey" || cookie["in"] != "cookie" || cookie["name"] != "soseek_admin_session" {
		t.Errorf("unexpected management cookie security definition: %v", cookie)
	}
	if doc["security"] != nil {
		t.Error("OpenAPI must not declare global auth that could make login require a cookie or alter caller auth")
	}
	login := paths["/admin/api/session"].(map[string]any)["post"].(map[string]any)
	loginResponse := login["responses"].(map[string]any)["200"].(map[string]any)
	if _, ok := loginResponse["headers"].(map[string]any)["Set-Cookie"]; !ok {
		t.Error("successful login must document the session Set-Cookie header")
	}
	rotate := paths["/admin/api/principals/{id}/rotate"].(map[string]any)["post"].(map[string]any)
	if !strings.Contains(rotate["description"].(string), "returned once") || !strings.Contains(rotate["description"].(string), "current administrator") {
		t.Error("rotation must document one-time plaintext and self-rotation lockout")
	}
}

func hasSecurityScheme(security []any, name string) bool {
	for _, raw := range security {
		requirements, ok := raw.(map[string]any)
		if ok {
			if _, exists := requirements[name]; exists {
				return true
			}
		}
	}
	return false
}

func hasRequiredHeader(parameters []any, name string) bool {
	for _, raw := range parameters {
		parameter, ok := raw.(map[string]any)
		if ok && parameter["in"] == "header" && parameter["name"] == name && parameter["required"] == true {
			return true
		}
	}
	return false
}
