package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

const fixtureKey = "test-upstream-secret-never-real"

func channel(t *testing.T, provider, base string) core.Channel {
	t.Helper()
	name := "SOSEEK_TEST_PROVIDER_KEY"
	t.Setenv(name, fixtureKey)
	return core.Channel{ID: provider + "-test", Provider: provider, BaseURL: base, KeyEnv: name, Model: "grok-test", TimeoutSeconds: 2}
}

func decodeRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode request: %v", err)
	}
	return body
}

func TestNativeRequestMapping(t *testing.T) {
	cases := []struct {
		name, provider, path, reply string
		call                        core.Call
		want                        map[string]any
		source                      string
		known                       bool
		micros                      int64
	}{
		{"exa-search", "exa", "/search", `{"results":[{"url":"https://go.dev/doc/","title":"Go","text":"body","publishedDate":"2026-01-01"}],"costDollars":{"total":0.007}}`, core.Call{Operation: "search", Query: "Go", Limit: 3, Options: map[string]any{"exa.category": "research paper"}}, map[string]any{"query": "Go", "numResults": float64(3), "category": "research paper"}, "https://go.dev/doc/", true, 7000},
		{"exa-read", "exa", "/contents", `{"results":[{"url":"https://go.dev/","text":"docs"}],"costDollars":{"total":0}}`, core.Call{Operation: "read", URL: "https://go.dev/"}, map[string]any{"ids": []any{"https://go.dev/"}, "text": true}, "https://go.dev/", true, 0},
		{"exa-similar", "exa", "/findSimilar", `{"results":[{"url":"https://go.dev/"}]}`, core.Call{Operation: "exa.find_similar", URL: "https://rust-lang.org/", Limit: 2}, map[string]any{"url": "https://rust-lang.org/", "numResults": float64(2)}, "https://go.dev/", false, 0},
		{"exa-answer", "exa", "/answer", `{"answer":"answer","citations":[{"url":"https://go.dev/"}]}`, core.Call{Operation: "answer", Query: "Go"}, map[string]any{"query": "Go"}, "https://go.dev/", false, 0},
		{"tavily-search", "tavily", "/search", `{"results":[{"url":"https://go.dev/","content":"snippet"}],"usage":{"credits":1}}`, core.Call{Operation: "search", Query: "Go", Limit: 3}, map[string]any{"query": "Go", "max_results": float64(3), "include_usage": true}, "https://go.dev/", false, 0},
		{"tavily-read", "tavily", "/extract", `{"results":[{"url":"https://go.dev/","raw_content":"body"}]}`, core.Call{Operation: "read", URL: "https://go.dev/"}, map[string]any{"urls": []any{"https://go.dev/"}, "include_usage": true}, "https://go.dev/", false, 0},
		{"tavily-map", "tavily", "/map", `{"results":["https://go.dev/doc/"]}`, core.Call{Operation: "map", URL: "https://go.dev/", Limit: 3}, map[string]any{"url": "https://go.dev/", "limit": float64(3), "include_usage": true}, "https://go.dev/doc/", false, 0},
		{"tavily-crawl", "tavily", "/crawl", `{"results":[{"url":"https://go.dev/","raw_content":"body"}]}`, core.Call{Operation: "crawl", URL: "https://go.dev/", Limit: 2}, map[string]any{"url": "https://go.dev/", "limit": float64(2), "include_usage": true}, "https://go.dev/", false, 0},
		{"firecrawl-search", "firecrawl", "/v2/search", `{"success":true,"data":{"web":[{"url":"https://go.dev/","title":"Go"}]}}`, core.Call{Operation: "search", Query: "Go", Limit: 3}, map[string]any{"query": "Go", "limit": float64(3)}, "https://go.dev/", false, 0},
		{"firecrawl-read", "firecrawl", "/v2/scrape", `{"success":true,"data":{"markdown":"body","metadata":{"sourceURL":"https://go.dev/","title":"Go"}}}`, core.Call{Operation: "read", URL: "https://go.dev/"}, map[string]any{"url": "https://go.dev/", "formats": []any{"markdown"}}, "https://go.dev/", false, 0},
		{"firecrawl-map", "firecrawl", "/v2/map", `{"success":true,"links":[{"url":"https://go.dev/doc/","title":"Docs"}]}`, core.Call{Operation: "map", URL: "https://go.dev/", Limit: 3}, map[string]any{"url": "https://go.dev/", "limit": float64(3)}, "https://go.dev/doc/", false, 0},
		{"serper-search", "serper", "/search", `{"organic":[{"link":"https://go.dev/","title":"Go"}],"credits":1}`, core.Call{Operation: "search", Query: "Go", Limit: 3}, map[string]any{"q": "Go", "num": float64(3)}, "https://go.dev/", false, 0},
		{"serper-news", "serper", "/news", `{"news":[{"link":"https://go.dev/","title":"Go"}]}`, core.Call{Operation: "news", Query: "Go"}, map[string]any{"q": "Go"}, "https://go.dev/", false, 0},
		{"serper-images", "serper", "/images", `{"images":[{"link":"https://go.dev/","imageUrl":"https://go.dev/logo.png"}]}`, core.Call{Operation: "images", Query: "Go"}, map[string]any{"q": "Go"}, "https://go.dev/", false, 0},
		{"serper-scholar", "serper", "/scholar", `{"organic":[{"link":"https://go.dev/","title":"Go"}]}`, core.Call{Operation: "scholar", Query: "Go"}, map[string]any{"q": "Go"}, "https://go.dev/", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != tc.path {
					t.Errorf("request: %s %s", r.Method, r.URL.Path)
				}
				key := r.Header.Get("Authorization")
				if tc.provider == "exa" || tc.provider == "serper" {
					key = r.Header.Get("X-API-KEY")
				} else {
					key = strings.TrimPrefix(key, "Bearer ")
				}
				if key != fixtureKey {
					t.Error("missing credential")
				}
				if got := decodeRequest(t, r); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("body = %#v; want %#v", got, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			out, err := New().Call(context.Background(), channel(t, tc.provider, server.URL), tc.call)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Sources) != 1 || out.Sources[0].URL != tc.source {
				t.Fatalf("sources: %#v", out.Sources)
			}
			if out.Usage.CostKnown != tc.known || out.Usage.ReportedMicros != tc.micros {
				t.Fatalf("usage: %#v", out.Usage)
			}
			if !tc.known && out.Usage.UnsettledAttempts != 1 {
				t.Fatal("unknown charge was silently treated as free")
			}
			if out.Sources[0].Occurrences[0].Channel != tc.provider+"-test" {
				t.Fatal("source channel attribution missing")
			}
		})
	}
}

func TestValidationRejectsBeforeSending(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer server.Close()
	cases := []core.Call{
		{Operation: "search", Query: "query", Options: map[string]any{"secret_override": "anything"}},
		{Operation: "search", Query: "query", Options: map[string]any{"numResults": 1.5}},
		{Operation: "search", Query: "query", Limit: 2, Options: map[string]any{"numResults": 3}},
		{Operation: "search", Query: "query", Options: map[string]any{"contents": map[string]any{"text": map[string]any{"headers": map[string]any{"Authorization": "bad"}}}}},
		{Operation: "search", Query: "query", Input: map[string]any{"type": "not-a-mode"}},
		{Operation: "search", Options: map[string]any{"numResults": 1}},
		{Operation: "search", Query: "query", Options: map[string]any{"numResults": 1}, Input: map[string]any{"numResults": 1}},
	}
	ch := channel(t, "exa", server.URL)
	client := New()
	for _, call := range cases {
		if err := client.Validate(ch, call); err == nil {
			t.Fatalf("Validate accepted %#v", call)
		}
		_, err := client.Call(context.Background(), ch, call)
		if err == nil {
			t.Fatalf("Call accepted %#v", call)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid inputs sent %d requests", hits.Load())
	}
	valid := core.Call{Operation: "contents", Input: map[string]any{"ids": []string{"https://go.dev/"}, "summary": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}}}}
	if err := client.Validate(ch, valid); err != nil {
		t.Fatalf("native structured schema rejected: %v", err)
	}
}

func TestGrokNativeSearchRetainsEvidenceWithoutInventedRank(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Error(r.URL.Path)
		}
		body := decodeRequest(t, r)
		if body["tool_choice"] != "required" || body["max_tool_calls"] != float64(2) {
			t.Errorf("search control: %#v", body)
		}
		tool := object(array(body["tools"])[0])
		if !reflect.DeepEqual(object(tool["filters"])["allowed_domains"], []any{"go.dev"}) {
			t.Errorf("filters: %#v", tool)
		}
		_, _ = io.WriteString(w, `{"model":"grok-test-build","status":"completed","output":[{"type":"web_search_call","id":"ws1","status":"completed","action":{"query":"go","sources":[{"url":"https://go.dev/","title":"Go"}]}},{"type":"message","content":[{"type":"output_text","text":"Summary","annotations":[{"type":"url_citation","url":"https://go.dev/","title":"Go","start_index":0,"end_index":3},{"type":"url_citation","url":"https://go.dev/doc/","title":"Docs"}]}]}],"usage":{"input_tokens":120,"output_tokens":40,"cost_in_usd_ticks":179172400}}`)
	}))
	defer server.Close()
	out, err := New().Call(context.Background(), channel(t, "grok", server.URL+"/v1"), core.Call{Operation: "search", Query: "Go", Options: map[string]any{"allowed_domains": []string{"go.dev"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "Summary" || len(out.Sources) != 2 {
		t.Fatalf("output: %#v", out)
	}
	for _, source := range out.Sources {
		if source.Occurrences[0].Rank != 0 {
			t.Fatal("invented rank")
		}
		if source.Content != "" {
			t.Fatal("summary falsely labeled webpage content")
		}
	}
	if !strings.Contains(string(out.Raw), "start_index") || !strings.Contains(string(out.Raw), "ws1") {
		t.Fatal("retrieval trace lost")
	}
	if out.Usage.NativeCalls != 1 || out.Usage.ReportedMicros != 17918 || !out.Usage.CostKnown {
		t.Fatalf("usage: %#v", out.Usage)
	}
	if len(out.Citations) != 2 || out.Citations[0].AnswerText != "Summary" || out.Citations[0].StartIndex == nil || *out.Citations[0].StartIndex != 0 || out.Citations[0].EndIndex == nil || *out.Citations[0].EndIndex != 3 || out.Citations[0].OffsetUnit != "upstream" {
		t.Fatalf("normalized citations: %#v", out.Citations)
	}
	if out.Citations[1].StartIndex != nil || out.Citations[1].EndIndex != nil {
		t.Fatal("absent citation offsets were invented")
	}
	encoded, _ := json.Marshal(out.Citations)
	var citationJSON []map[string]any
	if err := json.Unmarshal(encoded, &citationJSON); err != nil {
		t.Fatal(err)
	}
	if start, exists := citationJSON[0]["start_index"]; !exists || start != float64(0) {
		t.Fatal("explicit zero offset lost in JSON")
	}
	if _, exists := citationJSON[1]["start_index"]; exists {
		t.Fatal("missing offset serialized as zero")
	}
	if len(out.RetrievalEvents) != 1 || out.RetrievalEvents[0].ID != "ws1" || out.RetrievalEvents[0].Type != "web_search_call" || out.RetrievalEvents[0].Status != "completed" || out.RetrievalEvents[0].Query != "go" || !reflect.DeepEqual(out.RetrievalEvents[0].URLs, []string{"https://go.dev/"}) {
		t.Fatalf("normalized retrieval events: %#v", out.RetrievalEvents)
	}
}

func TestGrokCitationsStayBoundToTheirOriginalTextPart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"web_search_call","id":"ws-search","status":"completed","action":{"type":"search","query":"Go 文档","sources":[{"url":"https://go.dev/","title":"Go"}]}},{"type":"web_search_call","id":"ws-open","status":"completed","action":{"type":"open_page","url":"https://go.dev/doc/","urls":["https://go.dev/doc/","https://go.dev/tour/"]}},{"type":"message","content":[{"type":"output_text","text":"中文😀引文甲","annotations":[{"type":"url_citation","url":"https://go.dev/","title":"Go 中文","start_index":0,"end_index":6}]},{"type":"output_text","text":"Second [ref].","annotations":[{"type":"url_citation","url":"https://go.dev/doc/","title":"Docs","start_index":7,"end_index":12}]}]}]}`)
	}))
	defer server.Close()
	out, err := New().Call(context.Background(), channel(t, "grok", server.URL), core.Call{Operation: "search", Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "中文😀引文甲\nSecond [ref]." {
		t.Fatalf("answer: %q", out.Answer)
	}
	index := func(value int) *int { return &value }
	want := []core.Citation{{URL: "https://go.dev/", Title: "Go 中文", AnswerText: "中文😀引文甲", StartIndex: index(0), EndIndex: index(6), OffsetUnit: "upstream"}, {URL: "https://go.dev/doc/", Title: "Docs", AnswerText: "Second [ref].", StartIndex: index(7), EndIndex: index(12), OffsetUnit: "upstream"}}
	if !reflect.DeepEqual(out.Citations, want) {
		t.Fatalf("citations=%#v; want %#v", out.Citations, want)
	}
	if len(out.RetrievalEvents) != 2 || out.RetrievalEvents[0].Query != "Go 文档" || out.RetrievalEvents[1].Query != "" || !reflect.DeepEqual(out.RetrievalEvents[1].URLs, []string{"https://go.dev/doc/", "https://go.dev/tour/"}) {
		t.Fatalf("events: %#v", out.RetrievalEvents)
	}
	for _, source := range out.Sources {
		if source.Occurrences[0].Rank != 0 {
			t.Fatal("retrieval order became ranking")
		}
	}
}

func TestCompletionRoundTripPreservesToolMessages(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Error(r.URL.Path)
		}
		body := decodeRequest(t, r)
		if body["tool_choice"] != "auto" {
			t.Fatal("planner is not autonomous")
		}
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"model":"grok-test","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{\"query\":\"Go\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
			return
		}
		msgs := array(body["messages"])
		if len(msgs) != 3 || object(msgs[2])["tool_call_id"] != "call-1" || object(msgs[2])["role"] != "tool" {
			t.Errorf("history: %#v", msgs)
		}
		if array(object(msgs[1])["tool_calls"])[0] == nil {
			t.Error("assistant function call lost")
		}
		_, _ = io.WriteString(w, `{"model":"grok-test","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Found https://go.dev/"}}],"usage":{"prompt_tokens":30,"completion_tokens":10,"cost_in_usd_ticks":0}}`)
	}))
	defer server.Close()
	client := New()
	ch := channel(t, "grok", server.URL)
	tools := []core.Tool{{Type: "function", Function: core.ToolDefinition{Name: "search", Parameters: map[string]any{"type": "object"}}}}
	messages := []core.Message{{Role: "user", Content: "find Go docs"}}
	first, err := client.Complete(context.Background(), ch, messages, tools, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if first.Usage.CostKnown || first.Usage.UnsettledAttempts != 1 {
		t.Fatal("absent fee treated as zero")
	}
	messages = append(messages, first.Message, core.Message{Role: "tool", ToolCallID: first.Message.ToolCalls[0].ID, Content: `{"url":"https://go.dev/"}`})
	last, err := client.Complete(context.Background(), ch, messages, tools, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if last.FinishReason != "stop" || !last.Usage.CostKnown || last.Usage.ReportedMicros != 0 {
		t.Fatalf("completion: %#v", last)
	}
}

func TestGrokNativeCallCountUsesMaximumWithoutDoubleCounting(t *testing.T) {
	for _, tc := range []struct {
		name, total string
		want        int64
	}{{"reported-exceeds-events", "4", 4}, {"events-exceed-reported", "1", 2}, {"negative-total-rejected", "-1", 2}, {"fractional-total-rejected", "1.5", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"status":"completed","output":[{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://go.dev/"}]}},{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://go.dev/doc/"}]}}],"usage":{"num_server_side_tools_used":%s,"server_side_tool_usage_details":{"web_search_calls":2,"x_search_calls":0,"x_posts_fetched":0,"x_users_fetched":0,"code_interpreter_calls":0,"file_search_calls":0,"mcp_calls":0,"document_search_calls":0,"image_generation_calls":0},"cost_in_usd_ticks":179172400}}`, tc.total)
			}))
			defer server.Close()
			out, err := New().Call(context.Background(), channel(t, "grok", server.URL), core.Call{Operation: "search", Query: "Go"})
			if err != nil {
				t.Fatal(err)
			}
			if out.Usage.NativeCalls != tc.want {
				t.Fatalf("native calls=%d, want=%d", out.Usage.NativeCalls, tc.want)
			}
			if out.Usage.ReportedMicros != 17918 || !out.Usage.CostKnown {
				t.Fatalf("cost changed: %#v", out.Usage)
			}
		})
	}
	for _, option := range []string{"enable_image_understanding", "enable_image_search", "x_search"} {
		if err := New().Validate(core.Channel{Provider: "grok"}, core.Call{Operation: "search", Query: "Go", Options: map[string]any{option: true}}); err == nil {
			t.Fatalf("unverified capability accepted: %s", option)
		}
	}
}

func TestRedirectCannotLeakAuthentication(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := New().Call(context.Background(), channel(t, "tavily", source.URL), core.Call{Operation: "search", Query: "test"})
	if err == nil || !strings.Contains(err.Error(), "upstream_redirect") {
		t.Fatalf("err: %v", err)
	}
	if leaked.Load() {
		t.Fatal("redirect followed")
	}
}

func TestErrorsAreClassifiedRedactedAndNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		uncertain bool
	}{{401, "upstream_authentication", false}, {402, "upstream_quota", false}, {403, "upstream_forbidden", false}, {429, "upstream_rate_limited", false}, {504, "upstream_timeout", true}, {500, "upstream_error", true}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, fixtureKey)
			}))
			defer server.Close()
			_, err := New().Call(context.Background(), channel(t, "exa", server.URL), core.Call{Operation: "search", Query: "test"})
			var upstream *core.Error
			if !errors.As(err, &upstream) || upstream.Code != tc.code || upstream.Uncertain != tc.uncertain {
				t.Fatalf("error: %#v", err)
			}
			if strings.Contains(err.Error(), fixtureKey) {
				t.Fatal("credential leaked")
			}
			if hits.Load() != 1 {
				t.Fatal("request retried")
			}
			if tc.status == 429 && upstream.RetryAfterSeconds != 30 {
				t.Fatal("Retry-After lost")
			}
		})
	}
}

func TestSuccessfulEchoIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"url": "https://example.com/", "text": "Bearer " + fixtureKey}}, "echo": []any{fixtureKey}})
	}))
	defer server.Close()
	out, err := New().Call(context.Background(), channel(t, "exa", server.URL), core.Call{Operation: "search", Query: "test"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), fixtureKey) {
		t.Fatal("credential echoed into output")
	}
}

func TestResponseSizeAndTimeoutAreUncertain(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		}))
		defer server.Close()
		_, err := New().Call(context.Background(), channel(t, "exa", server.URL), core.Call{Operation: "search", Query: "test"})
		var e *core.Error
		if !errors.As(err, &e) || e.Code != "upstream_response_too_large" || !e.Uncertain {
			t.Fatalf("err: %#v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := New().Call(ctx, channel(t, "exa", server.URL), core.Call{Operation: "search", Query: "test"})
		var e *core.Error
		if !errors.As(err, &e) || e.Code != "upstream_timeout" || !e.Uncertain {
			t.Fatalf("err: %#v", err)
		}
	})
}

func TestTavilyAsyncCrawlIsNotFalselyCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		_, _ = io.WriteString(w, `{"id":"job-1","status":"processing"}`)
	}))
	defer server.Close()
	_, err := New().Call(context.Background(), channel(t, "tavily", server.URL), core.Call{Operation: "crawl", URL: "https://go.dev/"})
	var e *core.Error
	if !errors.As(err, &e) || e.Code != "unsupported_async_operation" || !e.Uncertain {
		t.Fatalf("err: %#v", err)
	}
}

func TestFirecrawlPollAndSameOriginPagination(t *testing.T) {
	var requests atomic.Int32
	var checkpoint atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/v2/crawl" && r.Method == "POST" {
			_, _ = io.WriteString(w, `{"success":true,"id":"job-1","url":"https://untrusted.invalid/ignore"}`)
			return
		}
		if r.URL.Path != "/v2/crawl/job-1" || r.Method != "GET" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		if !checkpoint.Load() {
			t.Error("polled before job identifier was checkpointed")
		}
		if r.URL.Query().Get("skip") == "1" {
			_, _ = io.WriteString(w, `{"status":"completed","creditsUsed":2,"data":[{"markdown":"two","metadata":{"sourceURL":"https://go.dev/two"}}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "creditsUsed": 2, "next": "/v2/crawl/job-1?skip=1", "data": []any{map[string]any{"markdown": "one", "metadata": map[string]any{"sourceURL": "https://go.dev/one"}}}})
	}))
	defer server.Close()
	out, err := New().Call(context.Background(), channel(t, "firecrawl", server.URL+"/v2"), core.Call{Operation: "crawl", URL: "https://go.dev/", Limit: 2, Progress: func(kind string, data any) error {
		m := object(data)
		if kind != "upstream_job" || m["job_id"] != "job-1" {
			t.Errorf("invalid progress: %s %#v", kind, data)
		}
		checkpoint.Store(true)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || len(out.Sources) != 2 || out.Usage.Credits != 2 {
		t.Fatalf("result: %#v requests=%d", out, requests.Load())
	}
	if out.Usage.CostKnown {
		t.Fatal("credits treated as USD")
	}
}

func TestFirecrawlCheckpointFailureStopsPolling(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"success":true,"id":"job-1"}`)
	}))
	defer server.Close()
	_, err := New().Call(context.Background(), channel(t, "firecrawl", server.URL), core.Call{Operation: "crawl", URL: "https://go.dev/", Progress: func(string, any) error { return errors.New("disk unavailable") }})
	var e *core.Error
	if !errors.As(err, &e) || e.Code != "checkpoint_failed" || !e.Uncertain {
		t.Fatalf("err: %#v", err)
	}
	if hits.Load() != 1 {
		t.Fatal("continued upstream execution after persistence failed")
	}
}

func TestFirecrawlCreditsAreCumulativeAcrossPagination(t *testing.T) {
	for _, tc := range []struct {
		name               string
		credits            []any
		want               float64
		unknown, decreased bool
	}{
		{"later-page-newer-total", []any{2, 5}, 5, false, false},
		{"repeated-job-total", []any{5, 5}, 5, false, false},
		{"decreasing-total", []any{5, 2}, 5, false, true},
		{"initially-unknown", []any{-1, 5}, 5, true, false},
		{"missing-total", []any{nil, nil}, 0, true, false},
		{"zero-reported", []any{0, 0}, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					_, _ = io.WriteString(w, `{"success":true,"id":"job-1"}`)
					return
				}
				index := 0
				if r.URL.Query().Get("skip") == "1" {
					index = 1
				}
				body := map[string]any{"status": "completed", "data": []any{}}
				if tc.credits[index] != nil {
					body["creditsUsed"] = tc.credits[index]
				}
				if index == 0 {
					body["next"] = "/v2/crawl/job-1?skip=1"
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			out, err := New().Call(context.Background(), channel(t, "firecrawl", server.URL), core.Call{Operation: "crawl", URL: "https://go.dev/"})
			if err != nil {
				t.Fatal(err)
			}
			if out.Usage.Credits != tc.want || out.Usage.CostKnown || out.Usage.ReportedMicros != 0 || out.Usage.UnsettledAttempts != 1 {
				t.Fatalf("usage: %#v", out.Usage)
			}
			warnings := strings.Join(out.Warnings, "\n")
			if strings.Contains(warnings, "snapshots were unavailable") != tc.unknown || strings.Contains(warnings, "reports decreased") != tc.decreased {
				t.Fatalf("warnings: %s", warnings)
			}
			var raw map[string]any
			if err := json.Unmarshal(out.Raw, &raw); err != nil {
				t.Fatal(err)
			}
			if raw["credit_scope"] != "job_cumulative" || raw["credit_aggregation"] != "maximum_observed" || len(array(raw["usage_snapshots"])) != 2 || len(array(raw["pages"])) != 2 {
				t.Fatalf("raw usage evidence: %#v", raw)
			}
		})
	}
}

func TestFirecrawlPaginationFailureKeepsSubmittedJobUncertain(t *testing.T) {
	for _, mode := range []string{"rate-limit", "foreign-origin"} {
		t.Run(mode, func(t *testing.T) {
			var foreignHit atomic.Bool
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignHit.Store(true) }))
			defer foreign.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					_, _ = io.WriteString(w, `{"success":true,"id":"job-1"}`)
					return
				}
				if r.URL.RawQuery != "" {
					w.WriteHeader(429)
					return
				}
				next := "/v2/crawl/job-1?skip=1"
				if mode == "foreign-origin" {
					next = foreign.URL + next
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "creditsUsed": 1, "next": next, "data": []any{}})
			}))
			defer server.Close()
			_, err := New().Call(context.Background(), channel(t, "firecrawl", server.URL), core.Call{Operation: "crawl", URL: "https://go.dev/"})
			var e *core.Error
			if !errors.As(err, &e) || !e.Uncertain || e.Retryable {
				t.Fatalf("submitted job error must preserve unknown charges: %#v", err)
			}
			if foreignHit.Load() {
				t.Fatal("pagination forwarded credential to a foreign origin")
			}
		})
	}
}

func TestCredentialFileReadAndConfigurationFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(fixtureKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := readKey(core.Channel{KeyFile: file})
	if err != nil || key != fixtureKey {
		t.Fatal("could not read referenced key")
	}
	for _, ch := range []core.Channel{{}, {KeyFile: "/does/not/exist"}, {KeyEnv: "UNSET_SOSEEK_TEST_KEY"}, {KeyFile: file, KeyEnv: "ANY"}} {
		if _, err := readKey(ch); err == nil {
			t.Fatalf("invalid key reference accepted: %#v", ch)
		}
	}
	for _, base := range []string{"https://user:pass@example.com/", "https://example.com/?api_key=secret", "file:///tmp/key"} {
		if _, err := endpoint(base, "search"); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}

func TestDiscoveryIsImmutableAndInputsAreValidated(t *testing.T) {
	client := New()
	ops := client.Operations("exa")
	if len(ops) != 5 {
		t.Fatalf("operations: %d", len(ops))
	}
	ops[0].Schema["additionalProperties"] = true
	if err := client.Validate(core.Channel{Provider: "exa"}, core.Call{Operation: "answer", Query: "a", Options: map[string]any{"arbitrary": 1}}); err == nil {
		t.Fatal("discovery mutation changed validation")
	}
	for _, provider := range []string{"exa", "tavily", "firecrawl", "serper", "grok"} {
		for _, op := range client.Operations(provider) {
			if op.Schema["additionalProperties"] != false {
				t.Fatalf("%s.%s allows arbitrary input", provider, op.ID)
			}
		}
	}
}
