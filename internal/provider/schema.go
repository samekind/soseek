package provider

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/samekind/soseek/internal/core"
)

type schema = map[string]any

func s() schema                 { return schema{"type": "string", "minLength": 1} }
func b() schema                 { return schema{"type": "boolean"} }
func i(min, max int) schema     { return schema{"type": "integer", "minimum": min, "maximum": max} }
func n(min, max float64) schema { return schema{"type": "number", "minimum": min, "maximum": max} }
func a(item schema) schema      { return schema{"type": "array", "items": item, "maxItems": 1000} }
func boundedArray(item schema, min, max int) schema {
	return schema{"type": "array", "items": item, "minItems": min, "maxItems": max}
}
func e(values ...string) schema      { return schema{"type": "string", "enum": values} }
func either(values ...schema) schema { return schema{"anyOf": values} }
func o(props map[string]any, required ...string) schema {
	if required == nil {
		required = []string{}
	}
	return schema{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func extend(base schema, props map[string]any, required ...string) schema {
	p := map[string]any{}
	for k, v := range object(base["properties"]) {
		p[k] = v
	}
	for k, v := range props {
		p[k] = v
	}
	return o(p, required...)
}

// Protocol names follow the public Exa/Tavily/Firecrawl OpenAPI documents.
// Deprecated parameters explicitly ignored by an upstream are not advertised.
var jsonSchema = schema{"type": "object", "additionalProperties": true}
var exaLocation = either(s(), o(map[string]any{"country": s()}, "country"), o(map[string]any{"country": s(), "latitude": n(-90, 90), "longitude": n(-180, 180)}, "latitude", "longitude"))
var exaText = either(b(), o(map[string]any{"maxCharacters": i(1, 1000000), "includeHtmlTags": b(), "verbosity": e("compact", "standard", "full"), "includeSections": a(e("header", "navigation", "banner", "body", "sidebar", "footer", "metadata")), "excludeSections": a(e("header", "navigation", "banner", "body", "sidebar", "footer", "metadata"))}))
var exaContents = o(map[string]any{
	"text": exaText, "highlights": either(b(), o(map[string]any{"numSentences": i(1, 100), "maxCharacters": i(1, 1000000), "query": s()})),
	"summary":   either(b(), o(map[string]any{"query": s(), "schema": schema{"type": "object", "additionalProperties": true}})),
	"livecrawl": e("never", "fallback", "always", "auto", "preferred"), "livecrawlTimeout": i(1, 90000), "maxAgeHours": n(-1, 720), "snapshotAsOf": s(), "subpages": i(0, 100), "subpageTarget": either(s(), a(s())),
	"extras": o(map[string]any{"links": i(0, 1000), "imageLinks": i(0, 1000)}),
})
var exaFilters = map[string]any{
	"numResults": i(1, 100), "includeDomains": a(s()), "excludeDomains": a(s()), "startPublishedDate": s(), "endPublishedDate": s(),
	"includeText": a(s()), "excludeText": a(s()), "contents": exaContents,
}
var tavilyTree = o(map[string]any{
	"url": s(), "instructions": s(), "max_depth": i(1, 5), "max_breadth": i(1, 500), "limit": i(1, 500), "select_paths": a(s()), "select_domains": a(s()),
	"exclude_paths": a(s()), "exclude_domains": a(s()), "allow_external": b(), "include_usage": b(),
}, "url")
var fireFormats = a(either(e("markdown", "html", "rawHtml", "links", "images", "screenshot", "summary", "branding"), o(map[string]any{"type": e("json"), "prompt": s(), "schema": schema{"type": "object", "additionalProperties": true}}, "type")))
var fireScrape = o(map[string]any{
	"formats": fireFormats, "onlyMainContent": b(), "includeTags": a(s()), "excludeTags": a(s()), "waitFor": i(0, 30000), "timeout": i(1000, 300000),
	"mobile": b(), "skipTlsVerification": b(), "removeBase64Images": b(), "blockAds": b(), "proxy": e("basic", "stealth", "auto"), "maxAge": i(0, 604800000),
	"storeInCache": b(), "parsers": a(e("pdf")), "location": o(map[string]any{"country": s(), "languages": a(s())}),
})
var serperSearch = o(map[string]any{"q": s(), "gl": s(), "hl": s(), "location": s(), "num": i(1, 100), "page": i(1, 100), "tbs": s(), "autocorrect": b()}, "q")

var operationSchemas = map[string]map[string]schema{
	"exa": {
		"search":       extend(o(exaFilters), map[string]any{"query": s(), "type": e("auto", "neural", "fast", "deep", "keyword", "instant", "deep-lite", "deep-reasoning"), "category": s(), "userLocation": exaLocation, "additionalQueries": boundedArray(s(), 1, 10), "moderation": b(), "outputSchema": jsonSchema, "objective": s(), "systemPrompt": s()}, "query"),
		"contents":     extend(exaContents, map[string]any{"ids": boundedArray(s(), 1, 100)}, "ids"),
		"find_similar": extend(o(exaFilters), map[string]any{"url": s(), "excludeSourceDomain": b()}, "url"),
		"answer":       o(map[string]any{"query": s(), "text": b(), "systemPrompt": s(), "model": e("exa", "exa-pro", "exa-research", "exa-fast"), "userLocation": exaLocation, "outputSchema": jsonSchema}, "query"),
	},
	"tavily": {
		"search":  o(map[string]any{"query": s(), "search_depth": e("basic", "advanced", "fast", "ultra-fast"), "topic": e("general", "news", "finance"), "max_results": i(1, 20), "chunks_per_source": i(1, 3), "time_range": e("day", "week", "month", "year", "d", "w", "m", "y"), "start_date": s(), "end_date": s(), "include_answer": either(b(), e("basic", "advanced")), "include_raw_content": either(b(), e("markdown", "text")), "include_images": b(), "include_image_descriptions": b(), "include_favicon": b(), "include_domains": a(s()), "exclude_domains": a(s()), "country": s(), "auto_parameters": b(), "include_usage": b()}, "query"),
		"extract": o(map[string]any{"urls": either(s(), boundedArray(s(), 1, 20)), "query": s(), "extract_depth": e("basic", "advanced"), "chunks_per_source": i(1, 5), "include_images": b(), "include_favicon": b(), "format": e("markdown", "text"), "timeout": n(1, 60), "include_usage": b()}, "urls"),
		"map":     tavilyTree,
		"crawl":   extend(tavilyTree, map[string]any{"extract_depth": e("basic", "advanced"), "format": e("markdown", "text"), "include_images": b()}, "url"),
	},
	"firecrawl": {
		"search": o(map[string]any{"query": s(), "limit": i(1, 100), "sources": a(either(e("web", "images", "news"), o(map[string]any{"type": e("web", "images", "news")}, "type"))), "categories": a(either(s(), o(map[string]any{"type": s()}, "type"))), "tbs": s(), "location": s(), "country": s(), "timeout": i(1000, 300000), "ignoreInvalidURLs": b(), "scrapeOptions": fireScrape}, "query"),
		"scrape": extend(fireScrape, map[string]any{"url": s()}, "url"),
		"map":    o(map[string]any{"url": s(), "search": s(), "sitemap": e("skip", "include", "only"), "includeSubdomains": b(), "ignoreQueryParameters": b(), "limit": i(1, 100000), "timeout": i(1000, 300000)}, "url"),
		"crawl":  o(map[string]any{"url": s(), "limit": i(1, 10000), "maxDiscoveryDepth": i(0, 100), "includePaths": a(s()), "excludePaths": a(s()), "allowBackwardLinks": b(), "allowExternalLinks": b(), "crawlEntireDomain": b(), "sitemap": e("skip", "include", "only"), "delay": n(0, 60), "maxConcurrency": i(1, 100), "scrapeOptions": fireScrape}, "url"),
	},
	"serper": {"search": serperSearch, "news": serperSearch, "images": serperSearch, "scholar": serperSearch},
	"grok":   {"search": o(map[string]any{"query": s(), "allowed_domains": boundedArray(s(), 0, 5), "excluded_domains": boundedArray(s(), 0, 5), "max_tool_calls": i(1, 10), "max_output_tokens": i(64, 32768)}, "query")},
}

func (c *Client) Operations(provider string) []core.Operation {
	ops := operationSchemas[provider]
	ids := make([]string, 0, len(ops))
	for id := range ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]core.Operation, 0, len(ids)+1)
	for _, id := range ids {
		// Return a copy so a consumer cannot mutate global validation rules.
		data, _ := json.Marshal(ops[id])
		var copied map[string]any
		_ = json.Unmarshal(data, &copied)
		out = append(out, core.Operation{ID: id, Description: provider + " native " + id, Schema: copied})
	}
	readID := map[string]string{"exa": "contents", "tavily": "extract", "firecrawl": "scrape"}[provider]
	if readID != "" {
		out = append(out, core.Operation{ID: "read", Description: "Read a URL using " + provider + " " + readID, Schema: o(map[string]any{"url": s()}, "url")})
	}
	if provider == "grok" {
		out = append(out, core.Operation{ID: "planner", Description: "Internal Grok function-calling completion", Schema: o(map[string]any{})})
	}
	return out
}

func prepare(provider, op string, call core.Call, spec schema) (map[string]any, error) {
	body := map[string]any{}
	for _, src := range []map[string]any{call.Input, call.Options} {
		for name, v := range src {
			name = strings.TrimPrefix(name, provider+".")
			if _, exists := body[name]; exists {
				return nil, core.E("invalid_input", "duplicate parameter: "+name)
			}
			body[name] = v
		}
	}
	set := func(name string, v any) error {
		if existing, ok := body[name]; ok {
			a, _ := json.Marshal(existing)
			b, _ := json.Marshal(v)
			if string(a) != string(b) {
				return core.E("invalid_input", "conflicting parameter: "+name)
			}
			return nil
		}
		body[name] = v
		return nil
	}
	if call.Query != "" {
		name := "query"
		if provider == "serper" {
			name = "q"
		}
		if err := set(name, call.Query); err != nil {
			return nil, err
		}
	}
	if call.URL != "" {
		name := "url"
		var value any = call.URL
		if provider == "exa" && op == "contents" {
			name = "ids"
			value = []string{call.URL}
		}
		if provider == "tavily" && op == "extract" {
			name = "urls"
			value = []string{call.URL}
		}
		if err := set(name, value); err != nil {
			return nil, err
		}
	}
	if call.Limit > 0 {
		name := ""
		switch provider {
		case "exa":
			if op == "search" || op == "find_similar" {
				name = "numResults"
			}
		case "tavily":
			if op == "search" {
				name = "max_results"
			} else if op == "map" || op == "crawl" {
				name = "limit"
			}
		case "firecrawl":
			if op != "scrape" {
				name = "limit"
			}
		case "serper":
			name = "num"
		}
		if name != "" {
			if err := set(name, call.Limit); err != nil {
				return nil, err
			}
		}
	}
	if provider == "exa" && op == "contents" {
		if _, exists := body["text"]; !exists {
			body["text"] = true
		}
	}
	if provider == "firecrawl" && op == "scrape" {
		if _, exists := body["formats"]; !exists {
			body["formats"] = []string{"markdown"}
		}
	}
	if provider == "tavily" {
		if _, exists := body["include_usage"]; !exists {
			body["include_usage"] = true
		}
	}
	// Normalize Go values to the same shape used by JSON input and schemas.
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, core.E("invalid_input", "parameters must be JSON values")
	}
	var normalized map[string]any
	dec := json.NewDecoder(strings.NewReader(string(encoded)))
	dec.UseNumber()
	if dec.Decode(&normalized) != nil {
		return nil, core.E("invalid_input", "parameters must be JSON values")
	}
	if err := validate(normalized, spec, "input"); err != nil {
		return nil, core.E("invalid_input", err.Error())
	}
	if provider == "grok" {
		if normalized["allowed_domains"] != nil && normalized["excluded_domains"] != nil {
			return nil, core.E("invalid_input", "allowed_domains and excluded_domains are mutually exclusive")
		}
		for _, name := range []string{"allowed_domains", "excluded_domains"} {
			if len(array(normalized[name])) > 5 {
				return nil, core.E("invalid_input", name+" permits at most 5 domains")
			}
		}
	}
	return normalized, nil
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func validate(value any, spec schema, path string) error {
	if alternatives, ok := spec["anyOf"].([]schema); ok {
		for _, alt := range alternatives {
			if validate(value, alt, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s has an unsupported value or shape", path)
	}
	switch spec["type"] {
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		props := object(spec["properties"])
		for key, v := range m {
			s, known := props[key]
			if !known {
				if spec["additionalProperties"] == true {
					continue
				}
				return fmt.Errorf("%s contains unknown parameter %q", path, key)
			}
			if err := validate(v, object(s), path+"."+key); err != nil {
				return err
			}
		}
		required, _ := spec["required"].([]string)
		for _, key := range required {
			if _, ok := m[key]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if max, ok := spec["maxItems"].(int); ok && len(items) > max {
			return fmt.Errorf("%s contains too many items", path)
		}
		if min, ok := spec["minItems"].(int); ok && len(items) < min {
			return fmt.Errorf("%s contains too few items", path)
		}
		for index, v := range items {
			if err := validate(v, object(spec["items"]), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if min, ok := spec["minLength"].(int); ok && len(strings.TrimSpace(v)) < min {
			return fmt.Errorf("%s must not be empty", path)
		}
		if values, ok := spec["enum"].([]string); ok {
			found := false
			for _, item := range values {
				if v == item {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s contains an unsupported choice", path)
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "integer", "number":
		v, ok := numeric(value)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s must be a number", path)
		}
		if spec["type"] == "integer" && math.Trunc(v) != v {
			return fmt.Errorf("%s must be an integer", path)
		}
		min, _ := numeric(spec["minimum"])
		max, hasMax := numeric(spec["maximum"])
		if v < min || (hasMax && v > max) {
			return fmt.Errorf("%s is outside the supported range", path)
		}
	}
	return nil
}
