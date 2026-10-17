package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/provider"
)

func TestStandardExaSearchDefaultsKeepBudgetAndNativeMeaning(t *testing.T) {
	for _, tc := range []struct {
		name         string
		request      core.Request
		wantContents any
		hasContents  bool
	}{
		{"standard", core.Request{Operation: "search", Provider: "exa", Query: "query"}, map[string]any{"highlights": map[string]any{"query": "query", "maxCharacters": float64(500)}}, true},
		{"explicit-false", core.Request{Operation: "search", Provider: "exa", Query: "query", Options: map[string]any{"exa.contents": map[string]any{"highlights": false}}}, map[string]any{"highlights": false}, true},
		{"native", core.Request{Operation: "exa.search", Input: map[string]any{"query": "query", "numResults": 2}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, db, up := fixture()
			e.cfg.Channels[0].CostBounds["search"] = 12345
			calls := 0
			up.call = func(_ context.Context, ch core.Channel, call core.Call) (core.Output, error) {
				calls++
				if ch.Provider != "exa" || call.Operation != "search" {
					t.Fatalf("unexpected extra request: %s.%s", ch.Provider, call.Operation)
				}
				contents, exists := call.Options["contents"]
				if !exists {
					contents, exists = call.Input["contents"]
				}
				if exists != tc.hasContents || !reflect.DeepEqual(contents, tc.wantContents) {
					t.Fatalf("contents=%#v exists=%v; want %#v", contents, exists, tc.wantContents)
				}
				return core.Output{Sources: []core.Source{{URL: "https://example.org/doc", Snippet: "Upstream evidence"}}, Usage: core.Usage{CostKnown: true, ReportedMicros: 7000}}, nil
			}
			result := run(t, e, tc.request, "defaults", "one")
			if result.Status != "succeeded" || calls != 1 || len(db.attempts) != 1 {
				t.Fatalf("search expanded into additional work: %+v calls=%d", result, calls)
			}
			if db.attempts[0].MaxMicros != 12345 || result.Usage.ReportedMicros != 7000 {
				t.Fatalf("configured reservation or reported cost changed: %+v %+v", db.attempts[0], result.Usage)
			}
		})
	}
}

func TestSearchDefaultsChangeCacheIdentityWithoutMutatingRequest(t *testing.T) {
	e, _, _ := fixture()
	s := &execution{e: e, run: core.Run{Request: core.Request{Profile: "quick"}}}
	ch := e.cfg.Channels[0]
	old := core.Call{Operation: "search", Query: "query", Limit: 8, Options: map[string]any{}}
	current := provider.StandardSearchCall(ch, old)
	if s.cacheKey(ch, old) == s.cacheKey(ch, current) {
		t.Fatal("old metadata-only search cache reused for highlighted search")
	}
	if len(old.Options) != 0 {
		t.Fatal("defaulting mutated request option map")
	}
	request := core.Request{Operation: "search", Provider: "exa", Query: "query"}
	before, _ := json.Marshal(request)
	plan, err := e.Plan(request, core.Principal{ID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(request)
	if string(before) != string(after) || len(plan.Request.Options) != 0 {
		t.Fatal("planning rewrote user-visible input")
	}
	delete(e.cfg.Channels[0].CostBounds, "search")
	if _, err := e.Plan(request, core.Principal{ID: "one"}); err == nil {
		t.Fatal("default highlights bypassed administrator cost declaration")
	}
}
