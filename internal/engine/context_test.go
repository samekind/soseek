package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/samekind/soseek/internal/core"
)

func TestReadTargetSurvivesSearchDomainLimitAndBodyBudgetDeduplicates(t *testing.T) {
	e, _, up := fixture()
	e.cfg.Limits.MaxChars = 10000
	s := &execution{e: e, run: core.Run{ID: "r", PrincipalID: "p", Principal: core.Principal{ID: "p"}, BudgetMicros: 100000, Request: core.Request{Operation: "research", Limit: 2, BudgetUSD: "0.100000"}}, sourceIDs: map[string]string{}}
	primary := core.Output{Sources: []core.Source{{URL: "https://example.org/a", Content: strings.Repeat("a", 6000)}}}
	s.ingest(primary, e.cfg.Channels[0], "search-a")
	s.ingest(primary, e.cfg.Channels[0], "search-a-repeat")
	if s.chars != 6000 {
		t.Fatalf("duplicate page charged %d bytes", s.chars)
	}
	s.ingest(core.Output{Sources: []core.Source{{URL: "https://example.org/b", Snippet: "second ranked source"}, {URL: "https://example.org/c", Snippet: "third ranked source"}}}, e.cfg.Channels[1], "search-b")
	up.call = func(_ context.Context, _ core.Channel, call core.Call) (core.Output, error) {
		return core.Output{Sources: []core.Source{{URL: call.URL, Content: "IMPORTANT_READ_TARGET " + strings.Repeat("b", 2000)}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	value, _ := s.executeTool(context.Background(), core.ToolCall{ID: "read-third", Function: core.ToolFunction{Name: "read", Arguments: `{"url":"https://example.org/c"}`}})
	encoded, _ := json.Marshal(value)
	var envelope struct {
		Result struct {
			Sources []core.Source `json:"untrusted_evidence"`
		} `json:"result"`
	}
	_ = json.Unmarshal(encoded, &envelope)
	if len(envelope.Result.Sources) != 1 || envelope.Result.Sources[0].URL != "https://example.org/c" || !strings.Contains(envelope.Result.Sources[0].Content, "IMPORTANT_READ_TARGET") {
		t.Fatal("read target replaced by global search view", string(encoded))
	}
	id := envelope.Result.Sources[0].ID
	found := false
	for _, source := range s.selectedSources() {
		if source.ID == id && len(source.Content) > 2000 {
			found = true
		}
	}
	if !found {
		t.Fatal("read source omitted from final evidence")
	}
	if s.chars != 8022 {
		t.Fatalf("unique content bytes=%d", s.chars)
	}
}

func TestContextCompactionPreservesPairsGoalEvidenceAndErrors(t *testing.T) {
	e, db, _ := fixture()
	s := &execution{e: e, run: core.Run{ID: "r", Request: core.Request{Query: "Compare engines using original sources and preserve version 18 constraint"}}, sourceIDs: map[string]string{}, readSources: map[string]bool{"S3": true}, excerpts: []excerpt{{SourceID: "S3", Start: 7000, End: 7026, Text: "EXACT_SELECTED_PASSAGE_TEXT"}}}
	for i := 1; i <= 3; i++ {
		s.sources = append(s.sources, core.Source{ID: fmt.Sprintf("S%d", i), URL: fmt.Sprintf("https://example.org/%d", i), Content: strings.Repeat("body ", 1000)})
	}
	messages := []core.Message{{Role: "system", Content: plannerInstruction}, {Role: "user", Content: s.run.Request.Query}}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("tool-%d", i)
		messages = append(messages, core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{ID: id, Type: "function", Function: core.ToolFunction{Name: "read", Arguments: `{"url":"https://example.org/3"}`}}}})
		payload, _ := json.Marshal(map[string]any{"tool_call_id": id, "tool": "read", "status": "partial", "errors": []core.Error{{Code: "earlier_provider_error", Message: "retained exact error"}}, "attempt_ids": []string{"attempt-" + id}, "local_only": false, "result": map[string]any{"untrusted_evidence": []core.Source{{ID: "S3", URL: "https://example.org/3", Content: strings.Repeat("REPEATED_TOOL_BODY ", 1400)}}, "source_views": map[string]any{"S3": map[string]any{"content_total_chars": 5000}}}})
		messages = append(messages, core.Message{Role: "tool", ToolCallID: id, Content: string(payload)})
	}
	before, _ := json.Marshal(messages)
	out, err := s.preparePlannerContext(context.Background(), messages, 4)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(out)
	if len(before) < 128000 || len(after) >= 128000 || !strings.Contains(string(after), s.run.Request.Query) || !strings.Contains(string(after), "EXACT_SELECTED_PASSAGE_TEXT") || !strings.Contains(string(after), "earlier_provider_error") {
		t.Fatalf("context lost required state or exceeded cap: before=%d after=%d", len(before), len(after))
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("tool-%d", i)
		calls, responses := 0, 0
		for _, m := range out {
			for _, call := range m.ToolCalls {
				if call.ID == id {
					calls++
				}
			}
			if m.Role == "tool" && m.ToolCallID == id {
				responses++
			}
		}
		if calls != 1 || responses != 1 {
			t.Fatal("assistant/tool pairing changed", id, calls, responses)
		}
	}
	logged := false
	for _, event := range db.events {
		if event.kind == "planner_context_compacted" {
			logged = true
			if strings.Contains(string(event.data), "EXACT_SELECTED_PASSAGE_TEXT") || strings.Contains(string(event.data), "REPEATED_TOOL_BODY") {
				t.Fatal("audit event leaked evidence text")
			}
		}
	}
	if !logged {
		t.Fatal("compaction not audited")
	}
}

func TestPlannerFailureHasActualStopReason(t *testing.T) {
	e, _, up := fixture()
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		return core.Completion{}, core.E("model_context_limit", "test execution envelope")
	}
	r := run(t, e, core.Request{Operation: "research", Query: "q"}, "r", "p")
	if r.StopReason != "model_context_limit" || r.Status != "failed" {
		t.Fatal("planner failure reported completed", r)
	}
}
