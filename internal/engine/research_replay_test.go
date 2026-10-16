package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/fusion"
)

// This optional replay uses only operator-supplied saved JSON and SSE events.
// A response pruned from the saved result cannot be reconstructed: a conspicuous
// placeholder tests routing/ID retention for that URL, not its factual content.
func TestReplaySavedResearchActions(t *testing.T) {
	resultPath, eventPath := os.Getenv("SOSEEK_RESEARCH_FIXTURE"), os.Getenv("SOSEEK_RESEARCH_EVENTS")
	if resultPath == "" || eventPath == "" {
		t.Skip("provide a saved result and its SSE events for offline action replay")
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Result core.Result `json:"result"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type action struct {
		Round     int            `json:"round"`
		ID        string         `json:"tool_call_id"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	actions := []action{}
	kind := ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if kind == "planner_tool_started" && strings.HasPrefix(line, "data: ") {
			var a action
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &a); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, a)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actions) == 0 {
		t.Fatal("saved trace contains no planner actions")
	}
	e, _, up := fixture()
	serper := e.cfg.Channels[0]
	serper.Provider = "serper"
	serper.ID = "serper"
	serper.QuotaGroup = "serper"
	e.cfg.Channels = append(e.cfg.Channels, serper)
	s := &execution{e: e, run: core.Run{ID: "offline-replay", PrincipalID: "replay", Principal: core.Principal{ID: "replay"}, BudgetMicros: 100000, Request: core.Request{Operation: "research", Limit: 12, BudgetUSD: "0.100000"}}, sourceIDs: map[string]string{}}
	byURL := map[string]core.Source{}
	for _, source := range saved.Result.Results {
		key := fusion.CanonicalURL(source.URL)
		byURL[key] = source
		s.sourceIDs[key] = source.ID
		var number int
		_, _ = fmt.Sscanf(source.ID, "S%d", &number)
		s.nextID = max(s.nextID, number)
	}
	activeID := ""
	missingBodies := map[string]bool{}
	up.call = func(_ context.Context, ch core.Channel, call core.Call) (core.Output, error) {
		out := core.Output{Usage: core.Usage{CostKnown: true}}
		if call.Operation == "read" {
			source, ok := byURL[fusion.CanonicalURL(call.URL)]
			if !ok {
				source = core.Source{URL: call.URL, Content: "[OFFLINE REPLAY PLACEHOLDER: original response body was not retained in the supplied result.]"}
				missingBodies[call.URL] = true
			}
			source.Occurrences = nil
			out.Sources = []core.Source{source}
			return out, nil
		}
		for _, source := range saved.Result.Results {
			for _, o := range source.Occurrences {
				if o.QueryID == activeID && o.Provider == ch.Provider {
					source.Occurrences = []core.Occurrence{o}
					out.Sources = append(out.Sources, source)
					break
				}
			}
		}
		return out, nil
	}
	messages := []core.Message{{Role: "system", Content: plannerInstruction}, {Role: "user", Content: "Replay the saved research goal using supplied evidence and preserve every read target."}}
	previous := map[string]any{}
	maxBytes := 0
	readIDs := map[string]string{}
	for i := 0; i < len(actions); {
		round := actions[i].Round
		messages, err = s.preparePlannerContext(context.Background(), messages, round)
		if err != nil {
			t.Fatal(err)
		}
		end := i
		calls := []core.ToolCall{}
		for end < len(actions) && actions[end].Round == round {
			encoded, _ := json.Marshal(actions[end].Arguments)
			calls = append(calls, core.ToolCall{ID: actions[end].ID, Type: "function", Function: core.ToolFunction{Name: actions[end].Tool, Arguments: string(encoded)}})
			end++
		}
		messages = append(messages, core.Message{Role: "assistant", ToolCalls: calls})
		for j, tool := range calls {
			activeID = tool.ID
			key := toolFingerprint(tool)
			value, found := previous[key]
			if !found {
				value, _ = s.executeTool(context.Background(), tool)
				previous[key] = value
			}
			encoded, _ := json.Marshal(value)
			messages = append(messages, core.Message{Role: "tool", ToolCallID: tool.ID, Content: string(encoded)})
			if tool.Function.Name == "read" {
				var envelope struct {
					Result struct {
						Sources []core.Source `json:"untrusted_evidence"`
					} `json:"result"`
				}
				_ = json.Unmarshal(encoded, &envelope)
				target, _ := actions[i+j].Arguments["url"].(string)
				if len(envelope.Result.Sources) != 1 || fusion.CanonicalURL(envelope.Result.Sources[0].URL) != fusion.CanonicalURL(target) {
					t.Fatalf("read response hides requested target %s: %s", target, encoded)
				}
				readIDs[target] = envelope.Result.Sources[0].ID
			}
		}
		encoded, _ := json.Marshal(messages)
		maxBytes = max(maxBytes, len(encoded))
		if len(encoded) > 128000 {
			t.Fatalf("incremental replay exceeded unchanged context cap: %d", len(encoded))
		}
		i = end
	}
	final := s.selectedSources()
	for target, id := range readIDs {
		found := false
		for _, source := range final {
			if source.ID == id {
				found = true
			}
		}
		if !found {
			t.Fatal("read target missing from final evidence", target, id)
		}
	}
	t.Logf("replayed %d saved actions across %d read targets; max context=%d bytes; %d lost response bodies used explicit placeholders; no network or model calls", len(actions), len(readIDs), maxBytes, len(missingBodies))
}
