package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func TestStandardExaSearchRequestsQueryHighlightsInOneCall(t *testing.T) {
	var hits atomic.Int32
	const reply = `{"results":[{"url":"https://example.org/doc","title":"Documentation","highlights":["A query-relevant passage.","Another supporting excerpt."]}],"costDollars":{"total":0.012}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/search" || r.Method != "POST" {
			t.Errorf("unexpected extra operation %s %s", r.Method, r.URL.Path)
		}
		body := decodeRequest(t, r)
		want := map[string]any{"query": "query about retrieval", "numResults": float64(4), "category": "news", "contents": map[string]any{"highlights": map[string]any{"query": "query about retrieval", "maxCharacters": float64(500)}}}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("request body=%#v; want %#v", body, want)
		}
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	ch := channel(t, "exa", server.URL)
	options := map[string]any{"category": "news"}
	call := StandardSearchCall(ch, core.Call{Operation: "search", Query: "query about retrieval", Limit: 4, Options: options})
	out, err := New().Call(context.Background(), ch, call)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one search, made %d requests", hits.Load())
	}
	if len(out.Sources) != 1 || out.Sources[0].Snippet != "A query-relevant passage.\nAnother supporting excerpt." || out.Sources[0].Content != "" {
		t.Fatalf("normalized result: %#v", out.Sources)
	}
	if out.Usage.ReportedMicros != 12000 || !out.Usage.CostKnown {
		t.Fatalf("upstream monetary report was replaced with a free assumption: %#v", out.Usage)
	}
	if !strings.Contains(string(out.Raw), "Another supporting excerpt.") {
		t.Fatal("server audit response lost highlights")
	}
	if _, changed := options["contents"]; changed {
		t.Fatal("standard defaults mutated caller options")
	}
}

func TestStandardExaSearchPreservesEveryExplicitContentsChoice(t *testing.T) {
	cases := []struct {
		name, key string
		contents  any
		input     bool
	}{
		{"false", "contents", map[string]any{"highlights": false}, false},
		{"namespaced-false", "exa.contents", map[string]any{"highlights": false}, false},
		{"empty-object", "contents", map[string]any{}, false},
		{"explicit-text", "contents", map[string]any{"text": map[string]any{"maxCharacters": float64(100)}}, false},
		{"explicit-highlights", "contents", map[string]any{"highlights": map[string]any{"query": "custom focus", "maxCharacters": float64(42)}}, false},
		{"native-input-false", "contents", map[string]any{"highlights": false}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := decodeRequest(t, r)
				if !reflect.DeepEqual(body["contents"], tc.contents) {
					t.Errorf("explicit contents changed: %#v; want %#v", body["contents"], tc.contents)
				}
				_, _ = io.WriteString(w, `{"results":[{"url":"https://example.org/doc","title":"Only a title"}]}`)
			}))
			defer server.Close()
			ch := channel(t, "exa", server.URL)
			call := core.Call{Operation: "search", Query: "original query"}
			if tc.input {
				call.Input = map[string]any{tc.key: tc.contents}
			} else {
				call.Options = map[string]any{tc.key: tc.contents}
			}
			before, _ := json.Marshal(call)
			out, err := New().Call(context.Background(), ch, StandardSearchCall(ch, call))
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(call)
			if string(before) != string(after) {
				t.Fatal("explicit request was mutated")
			}
			if out.Sources[0].Snippet != "" {
				t.Fatal("a title was invented as a snippet")
			}
			if tc.name != "explicit-highlights" && strings.Contains(strings.Join(out.Warnings, " "), "despite requested highlights") {
				t.Fatal("disabled or absent highlights treated as requested")
			}
		})
	}
}

func TestNativeExaSearchRetainsOriginalPayloadWithoutDefaultContents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequest(t, r)
		if !reflect.DeepEqual(body, map[string]any{"query": "native query", "numResults": float64(2)}) {
			t.Errorf("native payload changed: %#v", body)
		}
		_, _ = io.WriteString(w, `{"results":[{"url":"https://example.org/","title":"Title"}]}`)
	}))
	defer server.Close()
	ch := channel(t, "exa", server.URL)
	call := core.Call{Operation: "exa.search", Input: map[string]any{"query": "native query", "numResults": 2}}
	out, err := New().Call(context.Background(), ch, StandardSearchCall(ch, call))
	if err != nil {
		t.Fatal(err)
	}
	if out.Sources[0].Snippet != "" {
		t.Fatal("native metadata-only source acquired fabricated text")
	}
}

func TestLegacyCompatibleExaResponseMayOmitHighlights(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/compat/search" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"results":[{"url":"https://example.org/a","title":"Only metadata"},{"url":"https://example.org/b","title":"Has excerpt","snippet":"An existing compatible-provider excerpt."}]}`)
	}))
	defer server.Close()
	ch := channel(t, "exa", server.URL+"/compat")
	out, err := New().Call(context.Background(), ch, StandardSearchCall(ch, core.Call{Operation: "search", Query: "query"}))
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || len(out.Sources) != 2 || out.Sources[0].Snippet != "" || out.Sources[1].Snippet != "An existing compatible-provider excerpt." {
		t.Fatalf("legacy result altered or extra calls made: %#v", out)
	}
	if !strings.Contains(strings.Join(out.Warnings, " "), "1 result(s) without a snippet") {
		t.Fatal("missing requested snippet not reported")
	}
	if out.Usage.CostKnown || out.Usage.UnsettledAttempts != 1 {
		t.Fatal("compatible channel with missing price was assumed free")
	}
}

func TestSearchDefaultsLeaveOtherProvidersAndOperationsUnchanged(t *testing.T) {
	for _, tc := range []struct{ provider, operation string }{{"tavily", "search"}, {"serper", "search"}, {"firecrawl", "search"}, {"grok", "search"}, {"exa", "contents"}, {"exa", "find_similar"}, {"exa", "exa.search"}} {
		call := core.Call{Operation: tc.operation, Query: "query", Options: map[string]any{"existing": "value"}}
		before, _ := json.Marshal(call)
		after, _ := json.Marshal(StandardSearchCall(core.Channel{Provider: tc.provider}, call))
		if string(before) != string(after) {
			t.Fatalf("changed %s.%s", tc.provider, tc.operation)
		}
	}
}

func TestLegacyExaRejectingHighlightOptionsIsNotRetriedSilently(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body := decodeRequest(t, r)
		if _, advanced := object(body["contents"])["highlights"].(map[string]any); advanced {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"url":"https://example.org/","title":"Legacy result"}]}`)
	}))
	defer server.Close()
	ch := channel(t, "exa", server.URL)
	_, err := New().Call(context.Background(), ch, StandardSearchCall(ch, core.Call{Operation: "search", Query: "query"}))
	var upstream *core.Error
	if !errors.As(err, &upstream) || upstream.Code != "upstream_invalid_request" || hits.Load() != 1 {
		t.Fatalf("unsupported default was hidden or retried: %v calls=%d", err, hits.Load())
	}
	// A caller can explicitly retain its legacy contents contract; the provider
	// does not make this potentially chargeable second request on its behalf.
	out, err := New().Call(context.Background(), ch, StandardSearchCall(ch, core.Call{Operation: "search", Query: "query", Options: map[string]any{"contents": map[string]any{"highlights": false}}}))
	if err != nil || hits.Load() != 2 || len(out.Sources) != 1 || out.Sources[0].Snippet != "" {
		t.Fatalf("explicit legacy contents did not work: %v %#v", err, out)
	}
}
