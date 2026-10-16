package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/core"
)

func (c *Client) exa(ctx context.Context, ch core.Channel, key, op string, body map[string]any) (core.Output, error) {
	path := op
	if op == "find_similar" {
		path = "findSimilar"
	}
	m, raw, err := c.request(ctx, ch, key, http.MethodPost, path, body)
	if err != nil {
		return core.Output{}, err
	}
	if op != "answer" {
		if _, ok := m["results"].([]any); !ok {
			return core.Output{}, protocolError("Exa response has no result array")
		}
	} else if m["answer"] == nil {
		return core.Output{}, protocolError("Exa answer response has no answer")
	}
	out := core.Output{Raw: raw, Usage: usage(m), Answer: str(m["answer"])}
	if len(array(m["warnings"])) > 0 {
		out.Warnings = append(out.Warnings, "Exa reported request warnings; inspect raw warnings before assuming filters took effect")
	}
	items := array(m["results"])
	if op == "answer" && len(items) == 0 {
		items = array(m["citations"])
	}
	for _, item := range items {
		v := object(item)
		snippet := firstString(v, "summary", "snippet")
		if snippet == "" {
			var highlights []string
			for _, h := range array(v["highlights"]) {
				if s := str(h); s != "" {
					highlights = append(highlights, s)
				}
			}
			snippet = strings.Join(highlights, "\n")
		}
		if address := str(v["url"]); address != "" {
			out.Sources = append(out.Sources, core.Source{URL: address, Title: str(v["title"]), Snippet: snippet, Content: str(v["text"]), PublishedAt: str(v["publishedDate"])})
		}
	}
	for _, item := range array(m["statuses"]) {
		v := object(item)
		if status := str(v["status"]); status != "" && status != "success" {
			out.Warnings = append(out.Warnings, "one or more requested Exa documents were not returned; see raw statuses")
			break
		}
	}
	return out, nil
}

func (c *Client) tavily(ctx context.Context, ch core.Channel, key, op string, body map[string]any) (core.Output, error) {
	m, raw, err := c.request(ctx, ch, key, http.MethodPost, op, body)
	if err != nil {
		return core.Output{}, err
	}
	if op == "crawl" {
		status := str(m["status"])
		if (m["id"] != nil || m["task_id"] != nil) && m["results"] == nil || status == "pending" || status == "processing" || status == "queued" {
			return core.Output{}, &core.Error{Code: "unsupported_async_operation", Message: "this Tavily endpoint returned an asynchronous crawl job; polling is not implemented and the job may still incur charges", Uncertain: true}
		}
	}
	if _, ok := m["results"].([]any); !ok {
		return core.Output{}, protocolError("Tavily response has no result array")
	}
	out := core.Output{Raw: raw, Usage: usage(m), Answer: str(m["answer"])}
	for _, item := range array(m["results"]) {
		if address := str(item); address != "" {
			out.Sources = append(out.Sources, core.Source{URL: address})
			continue
		}
		v := object(item)
		if address := str(v["url"]); address != "" {
			content := str(v["raw_content"])
			snippet := str(v["content"])
			if op == "extract" || op == "crawl" {
				if content == "" {
					content = snippet
				}
				snippet = ""
			}
			out.Sources = append(out.Sources, core.Source{URL: address, Title: str(v["title"]), Content: content, Snippet: snippet, PublishedAt: str(v["published_date"])})
		}
	}
	if len(array(m["failed_results"])) > 0 {
		out.Warnings = append(out.Warnings, "some Tavily URLs could not be extracted; see raw failed_results")
	}
	return out, nil
}

// A base URL may already name the API version, allowing proxy prefixes such as
// /firecrawl/v2. Public default configurations use https://api.firecrawl.dev/v2.
func firePath(ch core.Channel, path string) string {
	u, _ := url.Parse(ch.BaseURL)
	if u != nil {
		last := strings.TrimRight(u.Path, "/")
		if strings.HasSuffix(last, "/v2") || strings.HasSuffix(last, "/v1") {
			return path
		}
	}
	return "v2/" + path
}

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

func (c *Client) firecrawl(ctx context.Context, ch core.Channel, key, op string, body map[string]any, progress func(string, any) error) (core.Output, error) {
	m, raw, err := c.request(ctx, ch, key, http.MethodPost, firePath(ch, op), body)
	if err != nil {
		return core.Output{}, err
	}
	if success, ok := m["success"].(bool); ok && !success {
		return core.Output{}, &core.Error{Code: "upstream_operation_failed", Message: "Firecrawl reported an unsuccessful operation", Uncertain: true}
	}
	if op == "crawl" && m["id"] != nil {
		id := str(m["id"])
		if !jobIDPattern.MatchString(id) {
			return core.Output{}, &core.Error{Code: "upstream_protocol_error", Message: "Firecrawl returned an invalid job ID", Uncertain: true}
		}
		start := m
		path := firePath(ch, "crawl/"+id)
		credits := crawlCredits{}
		if progress != nil {
			if err := progress("upstream_job", map[string]any{"provider": ch.Provider, "channel": ch.ID, "job_id": id, "status_path": path, "status": "submitted"}); err != nil {
				return core.Output{}, &core.Error{Code: "checkpoint_failed", Message: "could not persist the Firecrawl job identifier; upstream job may still be running", Uncertain: true}
			}
		}
		for {
			// Poll only the configured origin and job path; never trust a job's URL.
			m, raw, err = c.request(ctx, ch, key, http.MethodGet, path, nil)
			if err != nil {
				if e, ok := err.(*core.Error); ok {
					copy := *e
					copy.Uncertain = true
					copy.Retryable = false
					return core.Output{}, &copy
				}
				return core.Output{}, err
			}
			credits.observe(m, "poll")
			switch str(m["status"]) {
			case "completed":
				data := append([]any{}, array(m["data"])...)
				pages := []any{m}
				seen := map[string]bool{}
				for next := str(m["next"]); next != ""; next = str(m["next"]) {
					nextPath, err := fireNextPath(ch, path, next)
					if err != nil {
						return core.Output{}, err
					}
					if seen[nextPath] || len(pages) >= 100 {
						return core.Output{}, &core.Error{Code: "upstream_protocol_error", Message: "Firecrawl crawl pagination exceeded its safe bound", Uncertain: true}
					}
					seen[nextPath] = true
					m, _, err = c.request(ctx, ch, key, http.MethodGet, nextPath, nil)
					if err != nil {
						if upstream, ok := err.(*core.Error); ok {
							copy := *upstream
							copy.Uncertain = true
							copy.Retryable = false
							return core.Output{}, &copy
						}
						return core.Output{}, &core.Error{Code: "upstream_error", Message: "Firecrawl pagination failed after the crawl was submitted", Uncertain: true}
					}
					pages = append(pages, m)
					credits.observe(m, "page")
					data = append(data, array(m["data"])...)
					if len(data) > 10000 {
						return core.Output{}, &core.Error{Code: "upstream_response_too_large", Message: "Firecrawl crawl exceeded 10000 documents", Uncertain: true}
					}
				}
				out := core.Output{Usage: core.Usage{Credits: float64(credits.maximum), UnsettledAttempts: 1}, Warnings: []string{"Firecrawl creditsUsed is cumulative for the job; credits record the maximum observed total, not a sum of pagination responses"}}
				if credits.unknown {
					out.Warnings = append(out.Warnings, "one or more Firecrawl credit snapshots were unavailable or invalid; raw usage snapshots are retained and no exact USD charge is known")
				}
				if credits.decreased {
					out.Warnings = append(out.Warnings, "Firecrawl cumulative credit reports decreased; credits retain the maximum observed value and require reconciliation")
				}
				for _, item := range data {
					if source, ok := fireSource(object(item)); ok {
						out.Sources = append(out.Sources, source)
					}
				}
				out.Raw, _ = json.Marshal(map[string]any{"submission": start, "pages": pages, "credit_scope": "job_cumulative", "credit_aggregation": "maximum_observed", "usage_snapshots": credits.snapshots})
				if len(out.Raw) > maxResponseBytes {
					return core.Output{}, &core.Error{Code: "upstream_response_too_large", Message: "combined Firecrawl crawl exceeds 8 MiB", Uncertain: true}
				}
				return out, nil
			case "failed", "cancelled":
				return core.Output{}, &core.Error{Code: "upstream_operation_failed", Message: "Firecrawl crawl ended without completion", Uncertain: true}
			case "scraping", "pending", "queued", "processing":
			default:
				return core.Output{}, &core.Error{Code: "upstream_protocol_error", Message: "Firecrawl returned an unknown crawl status", Uncertain: true}
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return core.Output{}, &core.Error{Code: "upstream_timeout", Message: "Firecrawl crawl polling stopped; upstream job may still be running", Uncertain: true}
			case <-timer.C:
			}
		}
	}
	out := core.Output{Raw: raw, Usage: usage(m)}
	if op == "scrape" && object(m["data"]) == nil {
		return core.Output{}, protocolError("Firecrawl scrape response has no document")
	}
	if op == "map" {
		if _, ok := m["links"].([]any); !ok {
			if _, ok := m["data"].([]any); !ok {
				return core.Output{}, protocolError("Firecrawl map response has no links")
			}
		}
	}
	if op == "search" && object(m["data"]) == nil {
		if _, ok := m["data"].([]any); !ok {
			return core.Output{}, protocolError("Firecrawl search response has no result data")
		}
	}
	if op == "crawl" && str(m["status"]) != "completed" {
		return core.Output{}, protocolError("Firecrawl crawl response has no job or completed result")
	}
	var items []any
	switch op {
	case "scrape":
		items = []any{m["data"]}
	case "map":
		items = array(m["links"])
		if len(items) == 0 {
			items = array(m["data"])
		}
	case "search":
		if values, ok := m["data"].([]any); ok {
			items = values
		} else {
			data := object(m["data"])
			for _, name := range []string{"web", "news", "images"} {
				items = append(items, array(data[name])...)
			}
		}
	case "crawl":
		items = array(m["data"])
	}
	for _, item := range items {
		if address := str(item); address != "" {
			out.Sources = append(out.Sources, core.Source{URL: address})
			continue
		}
		v := object(item)
		if op == "scrape" && v != nil && firstString(v, "url") == "" && firstString(object(v["metadata"]), "sourceURL", "url") == "" {
			v["url"] = body["url"]
		}
		if source, ok := fireSource(v); ok {
			out.Sources = append(out.Sources, source)
		}
	}
	return out, nil
}

// Firecrawl documents creditsUsed as the cost of the whole crawl, and the
// official controller reads it by request/job ID independently of skip/limit:
// https://github.com/firecrawl/firecrawl/blob/386330419c6924cc5d733e59abca9b857241acc4/apps/api/src/controllers/v2/crawl-status.ts#L201-L238
// Repeated pagination totals must not be summed. Observe all snapshots because
// billing may be updated after the first completed status response.
type crawlCredits struct {
	maximum     int64
	previous    int64
	hasPrevious bool
	unknown     bool
	decreased   bool
	snapshots   []any
}

func (c *crawlCredits) observe(response map[string]any, phase string) {
	value, present := response["creditsUsed"]
	snapshot := map[string]any{"phase": phase, "status": str(response["status"])}
	if present {
		snapshot["credits_used"] = value
	}
	count, valid := nonnegativeInteger(value)
	// Credits are exposed as float64 by the shared protocol; do not claim exact
	// integer accounting if an upstream exceeds the exact representation range.
	if count > 1<<53 {
		valid = false
	}
	if !valid {
		c.unknown = true
		snapshot["credit_status"] = "unknown"
	} else {
		snapshot["credit_status"] = "reported"
		if c.hasPrevious && count < c.previous {
			c.decreased = true
		}
		c.previous = count
		c.hasPrevious = true
		if count > c.maximum {
			c.maximum = count
		}
	}
	c.snapshots = append(c.snapshots, snapshot)
}

func nonnegativeInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	count, err := number.Int64()
	return count, err == nil && count >= 0
}

func fireNextPath(ch core.Channel, path, next string) (string, error) {
	base, err := endpoint(ch.BaseURL, path)
	if err != nil {
		return "", err
	}
	b, _ := url.Parse(base)
	n, err := url.Parse(next)
	if err != nil {
		return "", &core.Error{Code: "upstream_protocol_error", Message: "invalid Firecrawl pagination URL", Uncertain: true}
	}
	n = b.ResolveReference(n)
	if n.Scheme != b.Scheme || n.Host != b.Host || n.Path != b.Path || n.User != nil || n.Fragment != "" {
		return "", &core.Error{Code: "upstream_protocol_error", Message: "Firecrawl pagination attempted to leave its configured job", Uncertain: true}
	}
	q := n.Query()
	skip, err := strconv.Atoi(q.Get("skip"))
	if len(q) != 1 || len(q["skip"]) != 1 || err != nil || skip < 0 {
		return "", &core.Error{Code: "upstream_protocol_error", Message: "invalid Firecrawl pagination parameters", Uncertain: true}
	}
	return path + "?skip=" + strconv.Itoa(skip), nil
}

func fireSource(v map[string]any) (core.Source, bool) {
	meta := object(v["metadata"])
	address := firstString(v, "url", "link")
	if address == "" {
		address = firstString(meta, "sourceURL", "url")
	}
	if address == "" {
		return core.Source{}, false
	}
	title := str(v["title"])
	if title == "" {
		title = str(meta["title"])
	}
	snippet := str(v["description"])
	if snippet == "" {
		snippet = str(meta["description"])
	}
	return core.Source{URL: address, Title: title, Snippet: snippet, Content: firstString(v, "markdown", "html", "rawHtml"), PublishedAt: firstString(meta, "publishedTime", "publishedDate")}, true
}

func (c *Client) serper(ctx context.Context, ch core.Channel, key, op string, body map[string]any) (core.Output, error) {
	m, raw, err := c.request(ctx, ch, key, http.MethodPost, op, body)
	if err != nil {
		return core.Output{}, err
	}
	out := core.Output{Raw: raw, Usage: usage(m)}
	field := "organic"
	if op == "news" {
		field = "news"
	}
	if op == "images" {
		field = "images"
	}
	if m[field] == nil && m["searchParameters"] == nil && m["knowledgeGraph"] == nil && m["answerBox"] == nil {
		return core.Output{}, protocolError("Serper response has no search result fields")
	}
	for _, item := range array(m[field]) {
		v := object(item)
		address := firstString(v, "link", "url", "imageUrl")
		if address == "" {
			continue
		}
		source := core.Source{URL: address, Title: str(v["title"]), Snippet: str(v["snippet"]), PublishedAt: str(v["date"])}
		if position := int(num(v["position"])); position > 0 {
			source.Occurrences = []core.Occurrence{{Rank: position}}
		}
		out.Sources = append(out.Sources, source)
	}
	if answer := object(m["answerBox"]); answer != nil {
		out.Answer = firstString(answer, "answer", "snippet")
	}
	return out, nil
}

func (c *Client) grokSearch(ctx context.Context, ch core.Channel, key string, input map[string]any) (core.Output, error) {
	if ch.Model == "" {
		return core.Output{}, core.E("configuration_error", "Grok channel requires a model")
	}
	if input["allowed_domains"] != nil && input["excluded_domains"] != nil {
		return core.Output{}, core.E("invalid_input", "allowed_domains and excluded_domains are mutually exclusive")
	}
	tool := map[string]any{"type": "web_search"}
	filters := map[string]any{}
	for _, field := range []string{"allowed_domains", "excluded_domains"} {
		if v, ok := input[field]; ok {
			if len(array(v)) > 5 {
				return core.Output{}, core.E("invalid_input", field+" permits at most 5 domains")
			}
			filters[field] = v
		}
	}
	if len(filters) > 0 {
		tool["filters"] = filters
	}
	body := map[string]any{"model": ch.Model, "input": []map[string]any{{"role": "user", "content": input["query"]}}, "tools": []any{tool}, "tool_choice": "required", "max_tool_calls": 2, "max_output_tokens": 2048, "stream": false, "include": []string{"web_search_call.action.sources"}}
	for _, field := range []string{"max_tool_calls", "max_output_tokens"} {
		if v, ok := input[field]; ok {
			body[field] = v
		}
	}
	m, raw, err := c.request(ctx, ch, key, http.MethodPost, "responses", body)
	if err != nil {
		return core.Output{}, err
	}
	out := core.Output{Raw: raw, Usage: usage(m)}
	seen := map[string]bool{}
	add := func(v map[string]any) {
		address := str(v["url"])
		if address != "" && !seen[address] {
			seen[address] = true
			out.Sources = append(out.Sources, core.Source{URL: address, Title: str(v["title"])})
		}
	}
	var text []string
	completedSearches := 0
	var observedSearches int64
	for _, entry := range array(m["output"]) {
		item := object(entry)
		switch str(item["type"]) {
		case "web_search_call":
			observedSearches++
			if str(item["status"]) == "completed" {
				completedSearches++
			}
			action := object(item["action"])
			event := core.RetrievalEvent{ID: str(item["id"]), Type: str(item["type"]), Status: str(item["status"]), Query: firstString(action, "query")}
			if event.Query == "" {
				event.Query = str(item["query"])
			}
			eventURLs := map[string]bool{}
			addEventURL := func(address string) {
				if address != "" && !eventURLs[address] {
					eventURLs[address] = true
					event.URLs = append(event.URLs, address)
				}
			}
			if address := str(action["url"]); address != "" {
				addEventURL(address)
				add(map[string]any{"url": address})
			}
			for _, address := range array(action["urls"]) {
				addEventURL(str(address))
				if address := str(address); address != "" {
					add(map[string]any{"url": address})
				}
			}
			for _, source := range array(action["sources"]) {
				value := object(source)
				add(value)
				addEventURL(str(value["url"]))
			}
			out.RetrievalEvents = append(out.RetrievalEvents, event)
			if status := str(item["status"]); status != "completed" {
				out.Warnings = append(out.Warnings, "a native Grok search did not complete; inspect retrieval events")
			}
		case "message":
			for _, part := range array(item["content"]) {
				v := object(part)
				if str(v["type"]) == "output_text" {
					text = append(text, str(v["text"]))
				}
				for _, annotation := range array(v["annotations"]) {
					annotation := object(annotation)
					if str(annotation["type"]) == "url_citation" {
						add(annotation)
						if address := str(annotation["url"]); address != "" {
							citation := core.Citation{URL: address, Title: str(annotation["title"]), AnswerText: str(v["text"])}
							start, startOK := nonnegativeInteger(annotation["start_index"])
							end, endOK := nonnegativeInteger(annotation["end_index"])
							if startOK && int64(int(start)) == start {
								index := int(start)
								citation.StartIndex = &index
								citation.OffsetUnit = "upstream"
							}
							if endOK && int64(int(end)) == end {
								index := int(end)
								citation.EndIndex = &index
								citation.OffsetUnit = "upstream"
							}
							out.Citations = append(out.Citations, citation)
						}
					}
				}
			}
		}
	}
	if observedSearches > out.Usage.NativeCalls {
		out.Usage.NativeCalls = observedSearches
	}
	if completedSearches == 0 {
		return core.Output{}, &core.Error{Code: "upstream_search_missing", Message: "Grok returned no native search event; this is not a verified search result", Uncertain: true}
	}
	out.Answer = strings.Join(text, "\n")
	if str(m["status"]) == "incomplete" {
		out.Warnings = append(out.Warnings, "Grok search response is incomplete; see raw incomplete_details")
	}
	if actual := str(m["model"]); actual != "" && actual != ch.Model {
		out.Warnings = append(out.Warnings, fmt.Sprintf("requested model %s; upstream reported %s", ch.Model, actual))
	}
	return out, nil
}

func protocolError(message string) *core.Error {
	return &core.Error{Code: "upstream_protocol_error", Message: message, Uncertain: true}
}
