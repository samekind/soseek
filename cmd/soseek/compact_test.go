package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/samekind/soseek/internal/core"
)

func evidenceFixture() core.Result {
	zero, end := 0, 9
	return core.Result{
		SchemaVersion: "1.0", RunID: "run_evidence", Status: "partial", Operation: "answer", Answer: "保留完整结论 [S7]，以及另一份证据 [S42]。",
		Queries: []core.EvidenceQuery{{ID: "q2", Query: "缺口补查", Providers: []string{"exa"}}},
		Results: []core.Source{
			{ID: "S7", URL: "https://example.org/a", CanonicalURL: "https://example.org/a", Title: "证据甲", Snippet: strings.Repeat("甲🙂", 400), Content: strings.Repeat("正文甲🙂", 5000), FoundBy: []string{"exa"}, Occurrences: []core.Occurrence{{Provider: "exa", Channel: "direct", QueryID: "q2", Rank: 1}}, Truncated: true},
			{ID: "S42", URL: "https://example.org/b", Title: "证据乙", Snippet: "short", Content: "short saved content"},
		},
		Citations:       []core.Citation{{SourceID: "S7", URL: "https://example.org/a", AnswerText: strings.Repeat("native upstream answer ", 1000), StartIndex: &zero, EndIndex: &end, OffsetUnit: "unicode_code_points"}, {SourceID: "UNUSED", URL: "https://example.org/unrelated", AnswerText: "not the synthesized answer"}},
		RetrievalEvents: []core.RetrievalEvent{{Type: "web_search_call", Query: "native retrieval query"}},
		Usage:           core.Usage{CostKnown: false, ReportedMicros: 7000, ReservedMicros: 50000, UnsettledAttempts: 1},
		Attempts: []core.Attempt{
			{Provider: "exa", Channel: "direct", Operation: "search", Status: "succeeded", Usage: core.Usage{CostKnown: true, ReportedMicros: 7000}},
			{Provider: "tavily", Channel: "shared", Operation: "search", Status: "uncertain", Error: &core.Error{Code: "upstream_timeout", Message: "response lost", Uncertain: true}, Usage: core.Usage{ReservedMicros: 50000, UnsettledAttempts: 1}},
		},
		Warnings: []string{"A request may have been billed."}, Gaps: []string{"One opposing source could not be fetched."}, StopReason: "budget_limit", Truncated: true, Raw: json.RawMessage(`{"native":"provider-specific-value"}`),
	}
}
func asJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCompactPreservesAnswerEvidenceLinksAndRisk(t *testing.T) {
	result := evidenceFixture()
	view, err := resultView(result, &options{}, "https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	v := asJSONMap(t, view)
	if v["answer"] != result.Answer || v["status"] != "partial" || v["stop_reason"] != "budget_limit" {
		t.Fatal("answer or completion boundary changed")
	}
	if len(v["warnings"].([]any)) != 1 || len(v["gaps"].([]any)) != 1 || v["usage"].(map[string]any)["unsettled_attempts"] != float64(1) {
		t.Fatal("risk or accounting removed")
	}
	issues := v["attempt_issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["error"].(map[string]any)["uncertain"] != true {
		t.Fatalf("uncertain attempt lost: %v", issues)
	}
	sources := v["results"].([]any)
	first := sources[0].(map[string]any)
	second := sources[1].(map[string]any)
	if first["source_id"] != "S7" || second["source_id"] != "S42" {
		t.Fatal("source IDs were renumbered")
	}
	if first["query_ids"].([]any)[0] != "q2" || first["found_by"].([]any)[0] != "exa" || v["queries"].([]any)[0].(map[string]any)["id"] != "q2" {
		t.Fatal("subquestion attribution removed")
	}
	if utf8.RuneCountInString(first["snippet"].(string)) != 500 || utf8.RuneCountInString(first["content"].(string)) != 1200 || first["snippet_truncated"] != true || first["content_truncated"] != true || first["source_content_truncated"] != true {
		t.Fatal("source text clipping is not explicit")
	}
	if _, ok := first["occurrences"]; ok {
		t.Fatal("rank details retained in compact mode")
	}
	citations := v["citations"].([]any)
	if len(citations) != 1 || citations[0].(map[string]any)["source_id"] != "S7" || !strings.Contains(v["citation_mode"].(string), "reference-only") {
		t.Fatal("citation mapping is not a reference-only cited-source map")
	}
	if _, ok := citations[0].(map[string]any)["start_index"]; ok {
		t.Fatal("native offset reattached to synthesized answer")
	}
	compact, _ := json.Marshal(view)
	full, _ := json.Marshal(result)
	if len(compact) >= len(full)/3 {
		t.Fatalf("large evidence response did not shrink: compact=%d full=%d", len(compact), len(full))
	}
	if v["output_truncated"] != true || v["source_content_truncated"] != true || v["full_result_command"] != "soseek runs get run_evidence --full --json" {
		t.Fatal("projection/upstream boundary or recovery command missing")
	}
}

func TestFullRestoresRawOriginalCitationsAndDoesNotPromiseMissingSourceText(t *testing.T) {
	result := evidenceFixture()
	view, err := resultView(result, &options{full: true}, "https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	v := asJSONMap(t, view)
	original := asJSONMap(t, result)
	for _, field := range []string{"raw", "results", "attempts", "answer", "queries", "citations", "retrieval_events", "truncated"} {
		if !reflect.DeepEqual(v[field], original[field]) {
			t.Errorf("full changed %s", field)
		}
	}
	citation := v["citations"].([]any)[0].(map[string]any)
	if citation["start_index"] != float64(0) || citation["offset_unit"] != "unicode_code_points" {
		t.Fatal("zero offset or original unit lost")
	}
	if v["output_truncated"] != false || v["source_content_truncated"] != true {
		t.Fatal("stored truncation mislabeled as output clipping")
	}
	if _, ok := v["full_result_command"]; ok {
		t.Fatal("full output promises a nonexistent fuller stored source")
	}
}

func TestExplicitFieldsUseFullDataKeepAccountingAndAnswerSourceIDs(t *testing.T) {
	result := evidenceFixture()
	view, err := resultView(result, &options{fields: "answer,content,raw"}, "https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	v := asJSONMap(t, view)
	first := v["results"].([]any)[0].(map[string]any)
	if first["content"] != result.Results[0].Content || v["raw"] == nil {
		t.Fatal("fields projected an already clipped compact response")
	}
	for _, key := range []string{"usage", "attempt_issues", "warnings", "gaps", "stop_reason", "queries"} {
		if v[key] == nil {
			t.Errorf("mandatory %s missing", key)
		}
	}
	answerOnly, _ := resultView(result, &options{fields: "answer"}, "https://cloud.example")
	refs := asJSONMap(t, answerOnly)["results"].([]any)
	if len(refs) != 2 || refs[0].(map[string]any)["source_id"] != "S7" {
		t.Fatal("answer-only projection broke source references")
	}
}

func TestReadCompactBodyAndUnicodeLimit(t *testing.T) {
	result := core.Result{RunID: "run_read", Status: "succeeded", Operation: "read", Results: []core.Source{{ID: "S9", URL: "https://example.org", Content: strings.Repeat("甲🙂", 7000)}}}
	compact, _ := resultView(result, &options{}, "https://cloud.example")
	source := asJSONMap(t, compact)["results"].([]any)[0].(map[string]any)
	if utf8.RuneCountInString(source["content"].(string)) != 12000 || source["content_total_chars"] != float64(14000) {
		t.Fatal("read body missing or wrong Unicode length")
	}
	clipped, _ := resultView(result, &options{maxChars: 3}, "https://cloud.example")
	source = asJSONMap(t, clipped)["results"].([]any)[0].(map[string]any)
	if source["content"] != "甲🙂甲" || source["content_total_chars"] != float64(14000) {
		t.Fatalf("original source length lost after second clip: %v", source)
	}
}

func TestSourceWindowsOnlyReadStoredEvidenceAndStopAtEnd(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method != "GET" || r.URL.Path != "/v1/runs/run_read/sources/S9" {
			t.Errorf("unexpected upstream-capable route: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		if r.URL.Query().Get("max_chars") != "2" {
			t.Error("source window length not forwarded")
		}
		switch r.URL.Query().Get("offset") {
		case "0":
			fmt.Fprint(w, `{"run_id":"run_read","source_id":"S9","url":"https://example.org","field":"content","text":"甲🙂","offset":0,"total_chars":4,"returned_chars":2,"next_offset":2,"truncated":true,"upstream_truncated":true}`)
		case "2":
			fmt.Fprint(w, `{"run_id":"run_read","source_id":"S9","url":"https://example.org","field":"content","text":"乙文","offset":2,"total_chars":4,"returned_chars":2,"truncated":true,"upstream_truncated":true}`)
		default:
			t.Error("unexpected offset")
			http.Error(w, "bad offset", 400)
		}
	}))
	defer server.Close()
	setupConfig(t, server.URL)
	code, out, stderr := invoke(t, "runs", "source", "run_read", "S9", "--max-chars", "2", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("first window %d %q %s", code, stderr, out)
	}
	first := oneObject(t, out)
	code, out, stderr = invoke(t, "runs", "source", "run_read", "S9", "--offset", "2", "--max-chars", "2", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("second window %d %q %s", code, stderr, out)
	}
	last := oneObject(t, out)
	if first["text"].(string)+last["text"].(string) != "甲🙂乙文" || first["next_offset"] != float64(2) || last["next_offset"] != nil || last["upstream_truncated"] != true {
		t.Fatal("Unicode windows do not reconstruct stored text or end is hidden")
	}
	if len(paths) != 2 {
		t.Fatalf("extra or paid request: %v", paths)
	}
}

func TestDiscoveryIsCompactUnlessFullSchemaRequested(t *testing.T) {
	schema := strings.Repeat("description text ", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := map[string]any{"id": "search", "description": "Search the web", "input_schema": map[string]any{"description": schema}}
		if strings.Contains(r.URL.Path, "/operations/") {
			_ = json.NewEncoder(w).Encode([]any{op})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"exa": []any{op}}, "channels": []any{map[string]any{"id": "direct", "provider": "exa", "available": true, "declared_operations": []string{"search"}}}, "revision": "test"})
	}))
	defer server.Close()
	setupConfig(t, server.URL)
	code, doctor, _ := invoke(t, "doctor", "--json")
	if code != 0 || len(doctor) > 500 {
		t.Fatalf("doctor is still noisy: %d %d", code, len(doctor))
	}
	if _, ok := oneObject(t, doctor)["capabilities"]; ok {
		t.Fatal("doctor printed schemas")
	}
	code, verbose, _ := invoke(t, "doctor", "--verbose", "--json")
	if code != 0 || oneObject(t, verbose)["capabilities"] == nil {
		t.Fatal("verbose doctor lost full discovery")
	}
	code, providers, _ := invoke(t, "providers", "--json")
	if code != 0 || strings.Contains(providers, "input_schema") || !strings.Contains(providers, "search") {
		t.Fatal("provider directory contains all schemas or lost operations")
	}
	code, operations, _ := invoke(t, "operations", "exa", "--json")
	if code != 0 || !strings.Contains(operations, "input_schema") {
		t.Fatal("operation schema unavailable")
	}
	code, capabilities, _ := invoke(t, "capabilities", "--json")
	if code != 0 || !strings.Contains(capabilities, "input_schema") {
		t.Fatal("full capabilities contract changed")
	}
}

func TestJSONSuccessHasNoStatusProgressUnlessRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(core.Run{ID: "run_quiet", Status: "succeeded", Result: &core.Result{RunID: "run_quiet", Status: "succeeded", Operation: "search", Usage: core.Usage{CostKnown: true}, Results: []core.Source{{ID: "S1", URL: "https://example.org"}}}})
	}))
	defer server.Close()
	setupConfig(t, server.URL)
	code, out, stderr := invoke(t, "search", "query", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("agent success has progress noise: %d %q", code, stderr)
	}
	oneObject(t, out)
	code, out, stderr = invoke(t, "search", "query", "--json", "--progress")
	if code != 0 || !strings.Contains(stderr, "run_quiet: succeeded") {
		t.Fatalf("requested progress missing: %d %q", code, stderr)
	}
	oneObject(t, out)
	code, out, stderr = invoke(t, "search", "query", "--json", "--fields", "title,url")
	if code != 0 {
		t.Fatalf("bare source aliases rejected: %d %s %s", code, out, stderr)
	}
	oneObject(t, out)
	code, _, _ = invoke(t, "search", "query", "--json", "--full", "--compact")
	if code != 2 {
		t.Fatal("conflicting output modes accepted")
	}
}

func TestPartialJSONKeepsWarningsAndUnknownChargesWithoutStatusChatter(t *testing.T) {
	result := evidenceFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(core.Run{ID: result.RunID, Status: result.Status, Result: &result})
	}))
	defer server.Close()
	setupConfig(t, server.URL)
	code, out, stderr := invoke(t, "runs", "wait", result.RunID, "--json", "--require-all")
	if code != 4 {
		t.Fatalf("partial require-all exit=%d", code)
	}
	view := oneObject(t, out)
	if view["status"] != "partial" || view["usage"].(map[string]any)["cost_known"] != false || view["attempt_issues"] == nil || view["gaps"] == nil || view["warnings"] == nil {
		t.Fatal("partial JSON lost actionable uncertainty")
	}
	if strings.Contains(stderr, result.RunID+": partial") || !strings.Contains(stderr, "partial result") {
		t.Fatalf("stderr hides warnings or includes status noise: %q", stderr)
	}
}

func TestSearchContentOnlyUsesAnUnmodifiedExcerpt(t *testing.T) {
	text := strings.Repeat("原文🙂", 250)
	result := core.Result{RunID: "run_search", Operation: "search", Results: []core.Source{{ID: "S23", URL: "https://example.org", Content: text}}}
	view, err := resultView(result, &options{}, "https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	source := asJSONMap(t, view)["results"].([]any)[0].(map[string]any)
	if source["snippet"] != string([]rune(text)[:500]) || source["snippet_from"] != "content" || source["evidence_type"] != "content_excerpt" || source["content_available"] != true || source["content"] != nil {
		t.Fatal("content-only result is empty or mislabeled")
	}
}

func TestTextBudgetNeverRetainsLargeNativeCitationTextOrOffsets(t *testing.T) {
	result := evidenceFixture()
	for _, o := range []*options{{full: true, maxChars: 20}, {fields: "citations,answer", maxChars: 20}} {
		view, err := resultView(result, o, "https://cloud.example")
		if err != nil {
			t.Fatal(err)
		}
		v := asJSONMap(t, view)
		if v["citations_truncated"] != true || v["output_truncated"] != true || v["full_result_command"] == nil {
			t.Fatal("citation clipping not recoverable")
		}
		for _, item := range v["citations"].([]any) {
			citation := item.(map[string]any)
			for _, field := range []string{"answer_text", "start_index", "end_index", "offset_unit"} {
				if _, ok := citation[field]; ok {
					t.Fatalf("budget retained native citation %s", field)
				}
			}
		}
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "native upstream answer") {
			t.Fatal("citation answer copy bypassed text budget")
		}
	}
}

func TestStoredSourceErrorKeepsRunIdentityAndStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `{"error":{"code":"expired","message":"stored source expired"}}`)
	}))
	defer server.Close()
	setupConfig(t, server.URL)
	code, out, _ := invoke(t, "runs", "source", "run_expired", "S3", "--json")
	if code != 4 {
		t.Fatalf("expired source exit %d", code)
	}
	detail := oneObject(t, out)["error"].(map[string]any)
	if detail["code"] != "expired" || detail["run_id"] != "run_expired" {
		t.Fatal("quiet output discarded failure code or known run ID")
	}
}
