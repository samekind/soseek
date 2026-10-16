package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/provider"
)

type memoryStore struct {
	mu          sync.Mutex
	attempts    []core.Attempt
	cache       map[string]core.Output
	cancelled   atomic.Bool
	checkpoints []core.Result
	events      []testEvent
}
type testEvent struct {
	kind string
	data json.RawMessage
}

func (m *memoryStore) BeginAttempt(_ context.Context, _ string, a core.Attempt) (core.Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ID = fmt.Sprintf("a%d", len(m.attempts)+1)
	m.attempts = append(m.attempts, a)
	return a, nil
}
func (m *memoryStore) EndAttempt(_ context.Context, a core.Attempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.attempts {
		if m.attempts[i].ID == a.ID {
			m.attempts[i] = a
		}
	}
	return nil
}
func (m *memoryStore) Attempts(_ context.Context, id string) ([]core.Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.Attempt
	for _, a := range m.attempts {
		if a.RunID == id {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memoryStore) AddEvent(_ context.Context, _ string, kind string, data any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	encoded, _ := json.Marshal(data)
	m.events = append(m.events, testEvent{kind: kind, data: encoded})
	return nil
}
func (m *memoryStore) CacheGet(_ context.Context, p, k string) (*core.Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, ok := m.cache[p+":"+k]
	if !ok {
		return nil, nil
	}
	return &out, nil
}
func (m *memoryStore) CachePut(_ context.Context, p, k string, o core.Output, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache == nil {
		m.cache = map[string]core.Output{}
	}
	m.cache[p+":"+k] = o
	return nil
}
func (m *memoryStore) Checkpoint(_ context.Context, _ string, v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := v.(core.Result); ok {
		m.checkpoints = append(m.checkpoints, r)
	}
	return nil
}
func (m *memoryStore) Cancelled(context.Context, string) bool { return m.cancelled.Load() }

type fakeProvider struct {
	call     func(context.Context, core.Channel, core.Call) (core.Output, error)
	complete func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error)
}

func (f *fakeProvider) Validate(ch core.Channel, call core.Call) error {
	return provider.New().Validate(ch, call)
}

func (f *fakeProvider) Call(ctx context.Context, ch core.Channel, c core.Call) (core.Output, error) {
	if f.call != nil {
		return f.call(ctx, ch, c)
	}
	return core.Output{Sources: []core.Source{{URL: "https://example.org/article", Content: "original evidence"}}, Usage: core.Usage{CostKnown: true}}, nil
}
func (f *fakeProvider) Complete(ctx context.Context, ch core.Channel, m []core.Message, t []core.Tool, n int) (core.Completion, error) {
	return f.complete(ctx, ch, m, t, n)
}
func (f *fakeProvider) Operations(string) []core.Operation {
	return []core.Operation{{ID: "search"}, {ID: "read"}, {ID: "planner"}, {ID: "contents"}, {ID: "find_similar"}}
}
func fixture() (*Engine, *memoryStore, *fakeProvider) {
	db := &memoryStore{}
	up := &fakeProvider{}
	cfg := core.Config{Revision: "test", SearchOrder: []string{"exa", "tavily", "grok"}, ReadOrder: []string{"firecrawl"}, PlannerChannel: "grok", Limits: core.Limits{MaxCalls: 12, MaxRounds: 6, MaxPages: 6, MaxChars: 60000, MaxDeadline: 180, DefaultRunMicros: 100000, MaxRunMicros: 100000, CacheSeconds: 300}}
	for _, p := range []string{"exa", "tavily", "firecrawl", "grok"} {
		cfg.Channels = append(cfg.Channels, core.Channel{ID: p, Provider: p, QuotaGroup: p, Operations: []string{"search", "read", "planner", "contents", "find_similar"}, CostBounds: map[string]int64{"search": 1000, "read": 1000, "planner": 5000, "contents": 1000, "find_similar": 1000}, Concurrency: 3, TimeoutSeconds: 2})
	}
	e := New(cfg, db, up)
	e.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	return e, db, up
}
func run(t *testing.T, e *Engine, req core.Request, id, principal string) core.Result {
	t.Helper()
	p := core.Principal{ID: principal}
	plan, err := e.Plan(req, p)
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := core.ParseUSD(plan.BudgetUSD)
	return e.Execute(context.Background(), core.Run{ID: id, Principal: p, Plan: plan, BudgetMicros: budget, Deadline: time.Now().Add(3 * time.Second)})
}

func TestPlanExplicitProviderAndNativeLimits(t *testing.T) {
	e, _, _ := fixture()
	p := core.Principal{ID: "one"}
	plan, err := e.Plan(core.Request{Operation: "search", Query: "q", Provider: "exa"}, p)
	if err != nil || len(plan.Steps) != 1 || plan.Steps[0].Provider != "exa" {
		t.Fatalf("%+v %v", plan, err)
	}
	plan, err = e.Plan(core.Request{Operation: "exa.find_similar", Input: map[string]any{"url": "https://example.org", "numResults": 20}}, p)
	if err != nil || plan.Request.Limit != 0 {
		t.Fatalf("native defaults changed supplied limit: %+v %v", plan, err)
	}
	_, err = e.Plan(core.Request{Operation: "search", Query: "q", Intent: "news"}, p)
	if err == nil {
		t.Fatal("unsupported intent accepted")
	}
	delete(e.cfg.Channels[0].CostBounds, "search")
	_, err = e.Plan(core.Request{Operation: "search", Query: "q", Provider: "exa"}, p)
	if err == nil {
		t.Fatal("unknown cost accepted")
	}
}
func TestExplicitProviderNeverFallsBack(t *testing.T) {
	e, _, up := fixture()
	var calls atomic.Int32
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{}, &core.Error{Code: "upstream_unavailable", Message: "before send", Retryable: true}
	}
	r := run(t, e, core.Request{Operation: "search", Query: "q", Provider: "exa"}, "r", "one")
	if calls.Load() != 1 || r.Status != "failed" {
		t.Fatalf("%d %+v", calls.Load(), r)
	}
}
func TestQuickFallbackRequiresKnownRetryableFailure(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			e, _, up := fixture()
			var calls atomic.Int32
			up.call = func(_ context.Context, ch core.Channel, _ core.Call) (core.Output, error) {
				calls.Add(1)
				if ch.Provider == "exa" {
					return core.Output{}, &core.Error{Code: "upstream_unavailable", Retryable: true, Uncertain: uncertain}
				}
				return core.Output{Sources: []core.Source{{URL: "https://example.org"}}, Usage: core.Usage{CostKnown: true}}, nil
			}
			r := run(t, e, core.Request{Query: "q"}, "r", "one")
			want := int32(2)
			if uncertain {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d result=%+v", calls.Load(), r)
			}
		})
	}
}
func TestParallelMergeAndPartialFailure(t *testing.T) {
	e, _, up := fixture()
	var active, maxActive atomic.Int32
	up.call = func(_ context.Context, ch core.Channel, _ core.Call) (core.Output, error) {
		n := active.Add(1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(15 * time.Millisecond)
		if ch.Provider == "tavily" {
			return core.Output{}, core.E("upstream_error", "failed")
		}
		return core.Output{Sources: []core.Source{{URL: "https://example.org/a"}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	r := run(t, e, core.Request{Query: "q", Providers: []string{"exa", "tavily", "grok"}}, "r", "one")
	if maxActive.Load() != 3 || len(r.Attempts) != 3 || r.Status != "partial" || len(r.Results) != 1 {
		t.Fatalf("active=%d result=%+v", maxActive.Load(), r)
	}
	for _, o := range r.Results[0].Occurrences {
		if o.Provider == "grok" && o.Rank != 0 {
			t.Fatal("Grok has invented rank")
		}
	}
}
func TestCachePrincipalIsolationAndNoNewCharge(t *testing.T) {
	e, _, up := fixture()
	var calls atomic.Int32
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{Sources: []core.Source{{URL: "https://example.org/a"}}, Usage: core.Usage{CostKnown: true, ReportedMicros: 7000}}, nil
	}
	req := core.Request{Query: "q", Provider: "exa"}
	run(t, e, req, "r1", "one")
	cached := run(t, e, req, "r2", "one")
	run(t, e, req, "r3", "two")
	if calls.Load() != 2 || len(cached.Attempts) != 1 || !cached.Attempts[0].Cached || cached.Usage.ReportedMicros != 0 {
		t.Fatalf("calls=%d cache=%+v", calls.Load(), cached)
	}
}
func TestSharedQuotaRateLimitStopsOtherChannel(t *testing.T) {
	e, _, up := fixture()
	e.cfg.Channels[0].QuotaGroup = "shared"
	e.cfg.Channels[1].QuotaGroup = "shared"
	var calls atomic.Int32
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{}, &core.Error{Code: "upstream_rate_limited", Retryable: true, RetryAfterSeconds: 60}
	}
	run(t, e, core.Request{Query: "q", Provider: "exa"}, "r1", "one")
	_, err := e.Plan(core.Request{Query: "q", Provider: "tavily"}, core.Principal{ID: "two"})
	if calls.Load() != 1 || err == nil {
		t.Fatalf("bypassed shared cooldown: %d %v", calls.Load(), err)
	}
}

func TestPipelineReadsEvidenceWithoutPlanner(t *testing.T) {
	e, _, up := fixture()
	var reads atomic.Int32
	up.call = func(_ context.Context, _ core.Channel, c core.Call) (core.Output, error) {
		if c.Operation == "read" {
			reads.Add(1)
			if c.URL == "" {
				t.Fatal("read lost URL")
			}
		}
		return core.Output{Sources: []core.Source{{URL: "https://example.org/a", Content: "document"}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		t.Fatal("pipeline started planner")
		return core.Completion{}, nil
	}
	r := run(t, e, core.Request{Operation: "research", Mode: "pipeline", Query: "q"}, "r", "one")
	if r.Status != "succeeded" || reads.Load() != 1 || len(r.Queries) != 1 {
		t.Fatalf("reads=%d result=%+v", reads.Load(), r)
	}
}

func TestUnknownOptionsAndWorkAmplificationFailDuringPlan(t *testing.T) {
	e, _, _ := fixture()
	p := core.Principal{ID: "one"}
	for _, req := range []core.Request{
		{Query: "q", Provider: "exa", Options: map[string]any{"exa.unrecognized": true}},
		{Query: "q", Provider: "exa", Options: map[string]any{"tavily.topic": "news"}},
		{Operation: "exa.contents", Input: map[string]any{"ids": []string{"https://one.example", "https://two.example"}, "subpages": 6}},
		{Query: "q", Provider: "grok", Options: map[string]any{"max_tool_calls": 10}},
	} {
		if _, err := e.Plan(req, p); err == nil {
			t.Fatalf("unsafe plan accepted: %+v", req)
		}
	}
	plan, err := e.Plan(core.Request{Query: "q", Options: map[string]any{"exa.category": "research paper"}}, p)
	if err != nil || len(plan.Steps) != 1 || plan.Steps[0].Provider != "exa" {
		t.Fatalf("provider-specific constraints diluted by fallback: %+v %v", plan, err)
	}
}

func TestCacheKeyIncludesQueryAndParameters(t *testing.T) {
	e, _, up := fixture()
	var calls atomic.Int32
	up.call = func(_ context.Context, _ core.Channel, c core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{Sources: []core.Source{{URL: "https://example.org/" + c.Query}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	run(t, e, core.Request{Query: "one", Provider: "exa"}, "one", "p")
	r := run(t, e, core.Request{Query: "two", Provider: "exa"}, "two", "p")
	if calls.Load() != 2 || r.Results[0].URL != "https://example.org/two" {
		t.Fatalf("cache key collision: %+v", r)
	}
}

func TestNativeSearchLimitIsSentAndViolationStopsPlanner(t *testing.T) {
	e, _, up := fixture()
	var rounds int
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		rounds++
		return tool("search", `{"query":"q","provider":"grok"}`, "search-native"), nil
	}
	up.call = func(_ context.Context, ch core.Channel, c core.Call) (core.Output, error) {
		if ch.Provider != "grok" || c.Input["max_tool_calls"] != 2 {
			t.Fatalf("native envelope not sent: %+v", c)
		}
		return core.Output{Sources: []core.Source{{URL: "https://example.org/a"}}, Usage: core.Usage{CostKnown: true, NativeCalls: 4, ReportedMicros: 500}}, nil
	}
	r := run(t, e, core.Request{Operation: "answer", Query: "q"}, "r", "one")
	if rounds != 1 || r.Status != "partial" || r.StopReason != "upstream_limit_exceeded" || r.Usage.NativeCalls != 4 || r.Answer != "" {
		t.Fatalf("upstream overrun hidden: %+v rounds=%d", r, rounds)
	}
}

func addExaProxy(e *Engine) {
	c := e.cfg.Channels[0]
	c.ID = "exa-proxy"
	c.QuotaGroup = "exa-proxy"
	e.cfg.Channels = append(e.cfg.Channels, c)
}
func TestSameProviderFallbackAndPinnedChannel(t *testing.T) {
	for _, pin := range []bool{false, true} {
		t.Run(fmt.Sprint(pin), func(t *testing.T) {
			e, _, up := fixture()
			addExaProxy(e)
			var channels []string
			up.call = func(_ context.Context, c core.Channel, _ core.Call) (core.Output, error) {
				channels = append(channels, c.ID)
				if c.ID == "exa" {
					return core.Output{}, &core.Error{Code: "upstream_unavailable", Retryable: true}
				}
				return core.Output{Sources: []core.Source{{URL: "https://example.org/a"}}, Usage: core.Usage{CostKnown: true}}, nil
			}
			req := core.Request{Query: "q", Provider: "exa"}
			if pin {
				req.Channel = "exa"
			}
			r := run(t, e, req, "r", "p")
			if pin {
				if len(channels) != 1 || r.Status != "failed" {
					t.Fatal(channels, r)
				}
			} else {
				if strings.Join(channels, ",") != "exa,exa-proxy" || r.Status != "succeeded" || len(r.Results) != 1 || r.Results[0].Occurrences[0].Channel != "exa-proxy" {
					t.Fatal(channels, r)
				}
				if len(r.Results[0].Occurrences) != 1 {
					t.Fatal("same provider fallback added votes")
				}
			}
		})
	}
}
func TestQuickFallbackChannelsShareTwoAttemptLimit(t *testing.T) {
	e, _, up := fixture()
	addExaProxy(e)
	var calls atomic.Int32
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{}, &core.Error{Code: "upstream_unavailable", Retryable: true}
	}
	r := run(t, e, core.Request{Query: "q"}, "r", "p")
	if calls.Load() != 2 || len(r.Attempts) != 2 {
		t.Fatal("quick exceeded two calls", r)
	}
}
func TestDisabledPrimaryExplainedAndExcluded(t *testing.T) {
	e, _, _ := fixture()
	addExaProxy(e)
	e.cfg.Channels[0].Disabled = true
	plan, err := e.Plan(core.Request{Query: "q", Provider: "exa"}, core.Principal{ID: "p"})
	if err != nil || plan.Steps[0].Channel != "exa-proxy" || !strings.Contains(strings.Join(plan.Warnings, " "), "exa excluded: disabled") {
		t.Fatal(plan, err)
	}
	_, err = e.Plan(core.Request{Query: "q", Provider: "exa", Channel: "exa"}, core.Principal{ID: "p"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("pin unavailable explanation missing", err)
	}
}
func TestInspectFindsMiddleOfSavedTextAndPreservesForSynthesis(t *testing.T) {
	e, _, up := fixture()
	content := strings.Repeat("unrelated introduction. ", 400) + "SQLite permits only one writer at a time in WAL mode." + strings.Repeat(" Later details.", 100)
	var upstream, rounds int
	up.call = func(_ context.Context, _ core.Channel, c core.Call) (core.Output, error) {
		upstream++
		source := core.Source{URL: "https://example.org/wal", Snippet: "WAL documentation"}
		if c.Operation == "read" {
			source.Content = content
		}
		return core.Output{Sources: []core.Source{source}, Usage: core.Usage{CostKnown: true}}, nil
	}
	up.complete = func(_ context.Context, _ core.Channel, m []core.Message, tools []core.Tool, _ int) (core.Completion, error) {
		rounds++
		switch rounds {
		case 1:
			return tool("search", `{"query":"WAL concurrency","provider":"exa"}`, "a"), nil
		case 2:
			return tool("read", `{"url":"https://example.org/wal"}`, "b"), nil
		case 3:
			encoded, _ := json.Marshal(m)
			if !strings.Contains(string(encoded), "content_total_chars") || !strings.Contains(string(encoded), "view_truncated") {
				t.Fatal("missing full-text navigation metadata")
			}
			return tool("inspect", `{"source_id":"S1","query":"only one writer","max_chars":1200}`, "c"), nil
		case 4:
			return tool("finish", `{"summary":"writer rule located","gaps":[]}`, "d"), nil
		case 5:
			encoded, _ := json.Marshal(m)
			if !strings.Contains(string(encoded), "SQLite permits only one writer at a time in WAL mode.") || !strings.Contains(string(encoded), "selected_passages") {
				t.Fatal("synthesis lost the middle passage")
			}
			return core.Completion{Message: core.Message{Content: "WAL mode still permits one writer at a time [S1]."}, Usage: core.Usage{CostKnown: true}}, nil
		}
		t.Fatal("unexpected round")
		return core.Completion{}, nil
	}
	r := run(t, e, core.Request{Operation: "answer", Query: "How many writers can WAL support?"}, "r", "p")
	if upstream != 2 || rounds != 5 || r.Status != "succeeded" || r.Answer == "" {
		t.Fatalf("inspect triggered new upstream or failed: upstream=%d rounds=%d result=%+v", upstream, rounds, r)
	}
}
func TestInspectCannotReadUnknownOrUnfetchedSource(t *testing.T) {
	e, _, _ := fixture()
	s := &execution{e: e, sources: []core.Source{{ID: "S1", URL: "https://example.org/a", Snippet: "not full text"}}}
	for _, id := range []string{"S1", "other-principal-S2"} {
		if _, err := s.inspect(inspectArgs{SourceID: id}); err == nil {
			t.Fatal("read unavailable evidence", id)
		}
	}
}

func TestSubpageTargetsValidateURLsButKeepKeywords(t *testing.T) {
	e, _, _ := fixture()
	e.resolve = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if ip := net.ParseIP(host); ip != nil {
			return []net.IPAddr{{IP: ip}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	for _, input := range []map[string]any{
		{"subpageTarget": "http://127.0.0.1/admin"},
		{"contents": map[string]any{"subpageTarget": []any{"pricing", "http://169.254.169.254/latest/meta-data"}}},
		{"exa.ids": []any{"https://[::ffff:127.0.0.1]"}},
		{"subpageTarget": "//127.0.0.1/private"},
		{"subpageTarget": "file:///etc/passwd"},
		{"allowExternalLinks": true},
		{"allow_external": true},
	} {
		if err := e.validateTargets(context.Background(), core.Call{Input: input}); err == nil {
			t.Fatalf("unsafe native targets accepted: %+v", input)
		}
	}
	if err := e.validateTargets(context.Background(), core.Call{Input: map[string]any{"subpageTarget": []any{"pricing", "contact us", "https://example.org/docs"}}}); err != nil {
		t.Fatal("valid keywords/public URL rejected", err)
	}
}

func TestNativeCitationsAndEventsSurviveLimitsCacheAndSynthesis(t *testing.T) {
	e, db, up := fixture()
	nativeAnswer := "Native Grok answer with an upstream citation."
	start, end := 0, 12
	up.call = func(_ context.Context, ch core.Channel, c core.Call) (core.Output, error) {
		if ch.Provider == "grok" {
			return core.Output{Sources: []core.Source{{URL: "https://a.example/first"}}, Answer: nativeAnswer, Citations: []core.Citation{{URL: "https://z.example/cited?utm_source=test", Title: "Cited document", AnswerText: nativeAnswer, StartIndex: &start, EndIndex: &end, OffsetUnit: "utf16_code_units"}}, RetrievalEvents: []core.RetrievalEvent{{ID: "search-event-1", Type: "web_search_call", Status: "completed", Query: "native query", URLs: []string{"https://z.example/cited"}}}, Usage: core.Usage{CostKnown: true, NativeCalls: 1}}, nil
		}
		return core.Output{Sources: []core.Source{{URL: c.URL, Content: "Retrieved supporting original text."}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		return core.Completion{Message: core.Message{Content: "Final synthesis citing the native source [S2]."}, Usage: core.Usage{CostKnown: true}}, nil
	}
	for _, id := range []string{"first", "cached"} {
		r := run(t, e, core.Request{Query: "q", Provider: "grok", Limit: 1}, id, "p")
		if len(r.Citations) != 1 || len(r.RetrievalEvents) != 1 || len(r.Results) != 2 || r.Citations[0].SourceID != "S2" || r.Citations[0].AnswerText != nativeAnswer || r.Citations[0].StartIndex == nil || *r.Citations[0].StartIndex != 0 {
			t.Fatalf("citation lost or rebound: %+v", r)
		}
		encoded, _ := json.Marshal(r.Citations[0])
		if !strings.Contains(string(encoded), `"start_index":0`) {
			t.Fatal("real zero offset omitted", string(encoded))
		}
	}
	r := run(t, e, core.Request{Operation: "answer", Mode: "pipeline", Query: "q", Provider: "grok", Limit: 1}, "answer", "p")
	if r.Answer == nativeAnswer || r.Citations[0].AnswerText != nativeAnswer || r.Citations[0].SourceID != "S2" || len(r.Results) != 2 {
		t.Fatal("upstream offsets confused with final answer", r)
	}
	last := db.checkpoints[len(db.checkpoints)-1]
	if len(last.Citations) != 1 || len(last.RetrievalEvents) != 1 {
		t.Fatal("checkpoint dropped native evidence")
	}
}

func TestToolEnvelopeCarriesImmediateErrorsAndLocalInspectHasNoAttempt(t *testing.T) {
	e, _, up := fixture()
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		return core.Output{}, &core.Error{Code: "upstream_rate_limited", Message: "current failure", RetryAfterSeconds: 10}
	}
	s := &execution{e: e, run: core.Run{ID: "r", PrincipalID: "p", Principal: core.Principal{ID: "p"}, BudgetMicros: 100000, Request: core.Request{BudgetUSD: "0.100000"}}, sourceIDs: map[string]string{}, warnings: []string{"old warning 1", "old warning 2", "old warning 3", "old warning 4", "old warning 5"}}
	value, _ := s.executeTool(context.Background(), core.ToolCall{ID: "failed-search", Function: core.ToolFunction{Name: "search", Arguments: `{"query":"q","provider":"exa"}`}})
	b, _ := json.Marshal(value)
	var envelope struct {
		Status     string       `json:"status"`
		Errors     []core.Error `json:"errors"`
		AttemptIDs []string     `json:"attempt_ids"`
		Count      int          `json:"upstream_attempt_count"`
	}
	_ = json.Unmarshal(b, &envelope)
	if envelope.Status != "failed" || len(envelope.Errors) != 1 || envelope.Errors[0].Code != "upstream_rate_limited" || envelope.Errors[0].RetryAfterSeconds != 10 || len(envelope.AttemptIDs) != 1 || envelope.Count != 1 {
		t.Fatal("immediate structured result missing", string(b))
	}
	s.sources = []core.Source{{ID: "S1", URL: "https://example.org", Content: "The text is already saved."}}
	value, _ = s.executeTool(context.Background(), core.ToolCall{ID: "local-inspect", Function: core.ToolFunction{Name: "inspect", Arguments: `{"source_id":"S1"}`}})
	b, _ = json.Marshal(value)
	var local struct {
		Status     string   `json:"status"`
		Local      bool     `json:"local_only"`
		Count      int      `json:"upstream_attempt_count"`
		AttemptIDs []string `json:"attempt_ids"`
	}
	_ = json.Unmarshal(b, &local)
	if local.Status != "succeeded" || !local.Local || local.Count != 0 || len(local.AttemptIDs) != 0 {
		t.Fatal("local inspect claimed an upstream attempt", string(b))
	}
}

func TestInspectSkipsContentsTrapAndMatchesWrappedText(t *testing.T) {
	e, _, _ := fixture()
	body := "目录\n2.2 Synchronization\n" + strings.Repeat("Introduction to the system. ", 300) + "\n# 2.2 Synchronization\nThe system permits one active writer\n\tat a time, while readers continue.\n" + strings.Repeat("Further details. ", 100)
	s := &execution{e: e, sources: []core.Source{{ID: "S1", URL: "https://example.org/doc", Content: body}}}
	value, err := s.inspect(inspectArgs{SourceID: "S1", Query: "Synchronization", MaxChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	view := value.(map[string]any)
	passages := view["passages"].([]excerpt)
	if view["match_count"] != 2 || len(passages) != 2 {
		t.Fatal("only first TOC occurrence returned", view)
	}
	foundBody := false
	for _, p := range passages {
		if string([]rune(body)[p.Start:p.End]) != p.Text {
			t.Fatal("original offsets corrupted")
		}
		if strings.Contains(p.Text, "one active writer") {
			foundBody = true
		}
	}
	if !foundBody {
		t.Fatal("body section still hidden by TOC")
	}
	value, err = s.inspect(inspectArgs{SourceID: "S1", Query: "ONE ACTIVE WRITER AT A TIME", MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["query_matched"] != true {
		t.Fatal("wrapped phrase not matched", value)
	}
	value, err = s.inspect(inspectArgs{SourceID: "S1", Query: "nonexistent exact phrase", MaxChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["query_matched"] != false || len(value.(map[string]any)["suggested_queries"].([]string)) == 0 {
		t.Fatal("no actionable no-match guidance")
	}
}

func TestInspectLaterMatchesAreNavigable(t *testing.T) {
	e, _, _ := fixture()
	body := ""
	for i := 0; i < 6; i++ {
		body += fmt.Sprintf("Section%d. Concurrency\n", i) + strings.Repeat("padding ", 150)
	}
	s := &execution{e: e, sources: []core.Source{{ID: "S1", URL: "https://example.org", Content: body}}}
	v, err := s.inspect(inspectArgs{SourceID: "S1", Query: "Concurrency", MaxChars: 1200})
	if err != nil {
		t.Fatal(err)
	}
	first := v.(map[string]any)
	next, ok := first["next_match_offset"].(*int)
	if !ok || next == nil || first["more_matches"] != true {
		t.Fatal("missing continuation for later hits", first)
	}
	v, err = s.inspect(inspectArgs{SourceID: "S1", Query: "Concurrency", Offset: *next, MaxChars: 1200})
	if err != nil {
		t.Fatal(err)
	}
	positions := v.(map[string]any)["matched_offsets"].([]int)
	if positions[0] != *next {
		t.Fatal("continuation repeated prior hit")
	}
}

func TestPlannerEventsPersistActionsAndSafeInspectionSummary(t *testing.T) {
	e, db, up := fixture()
	var rounds int
	body := "TOC: Synchronization\n" + strings.Repeat("intro ", 1500) + "\nSynchronization\nVISIBLE_EVIDENCE_MARKER The engine serializes writes."
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		return core.Output{Sources: []core.Source{{URL: "https://example.org/doc", Content: body}}, Usage: core.Usage{CostKnown: true}}, nil
	}
	up.complete = func(_ context.Context, _ core.Channel, messages []core.Message, _ []core.Tool, _ int) (core.Completion, error) {
		rounds++
		encoded, _ := json.Marshal(messages)
		if !strings.Contains(string(encoded), "decision_rounds_after_this") {
			t.Fatal("planner lacks live remaining limits")
		}
		switch rounds {
		case 1:
			return tool("search", `{"query":"synchronization","provider":"exa"}`, "search-1"), nil
		case 2:
			return tool("inspect", `{"source_id":"S1","query":"Synchronization","max_chars":1500}`, "inspect-1"), nil
		default:
			return tool("finish", `{"summary":"found the body section","gaps":[]}`, "finish-1"), nil
		}
	}
	r := run(t, e, core.Request{Operation: "research", Query: "Explain synchronization"}, "r", "p")
	if r.Status != "succeeded" {
		t.Fatal(r)
	}
	started, finished, inspected := 0, 0, false
	for _, event := range db.events {
		if strings.Contains(string(event.data), "VISIBLE_EVIDENCE_MARKER") {
			t.Fatal("page text leaked into planner events")
		}
		if event.kind == "planner_tool_started" {
			started++
		}
		if event.kind == "planner_tool_finished" {
			finished++
			var v map[string]any
			_ = json.Unmarshal(event.data, &v)
			if v["tool"] == "inspect" {
				summary := v["inspection"].(map[string]any)
				if summary["match_count"] != float64(2) || v["local_only"] != true || v["upstream_attempt_count"] != float64(0) {
					t.Fatal("inspection diagnostics missing", string(event.data))
				}
				inspected = true
			}
		}
	}
	if started != 3 || finished != 3 || !inspected {
		t.Fatal("planner tool events missing", started, finished)
	}
	args := safeToolArguments(core.ToolCall{Function: core.ToolFunction{Name: "read", Arguments: `{"url":"https://user:password@example.org/doc?api_key=private-value"}`}})
	encoded, _ := json.Marshal(args)
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "private-value") {
		t.Fatal("credentials leaked into action event", string(encoded))
	}
}
func TestUnsafeURLsNeverReachProvider(t *testing.T) {
	e, _, up := fixture()
	var calls atomic.Int32
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		calls.Add(1)
		return core.Output{}, nil
	}
	e.resolve = func(_ context.Context, h string) ([]net.IPAddr, error) {
		if h == "mixed.example" {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.2")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP(h)}}, nil
	}
	for i, u := range []string{"http://127.0.0.1", "http://[::ffff:127.0.0.1]", "https://mixed.example", "https://user:pass@example.org", "http://169.254.169.254"} {
		r := run(t, e, core.Request{Operation: "read", URL: u}, fmt.Sprint(i), "one")
		if r.Status != "failed" || len(r.Attempts) != 0 {
			t.Fatalf("unsafe URL reached attempt: %s %+v", u, r)
		}
	}
	r := run(t, e, core.Request{Operation: "exa.contents", Input: map[string]any{"ids": []any{"http://127.0.0.1"}}}, "ids", "one")
	if r.Status != "failed" || calls.Load() != 0 {
		t.Fatal("native URL bypass", r)
	}
}
func tool(name, args, id string) core.Completion {
	return core.Completion{Message: core.Message{Role: "assistant", ToolCalls: []core.ToolCall{{ID: id, Type: "function", Function: core.ToolFunction{Name: name, Arguments: args}}}}, Usage: core.Usage{CostKnown: true, ReportedMicros: 100}}
}
func TestAgentPerformsSearchReadFinishAndCitedAnswer(t *testing.T) {
	e, db, up := fixture()
	var rounds int
	up.complete = func(_ context.Context, _ core.Channel, m []core.Message, tools []core.Tool, _ int) (core.Completion, error) {
		rounds++
		switch rounds {
		case 1:
			return tool("search", `{"query":"primary evidence","provider":"grok"}`, "t1"), nil
		case 2:
			b, _ := json.Marshal(m)
			if !strings.Contains(string(b), "S1") {
				t.Fatal("source IDs not returned to planner")
			}
			return tool("read", `{"url":"https://example.org/article"}`, "t2"), nil
		case 3:
			return tool("finish", `{"summary":"covered","gaps":[]}`, "t3"), nil
		case 4:
			if len(tools) != 0 {
				t.Fatal("synthesis has tools")
			}
			return core.Completion{Message: core.Message{Content: "Supported claim [S1]."}, Usage: core.Usage{CostKnown: true}}, nil
		}
		t.Fatal("unexpected planner round")
		return core.Completion{}, nil
	}
	r := run(t, e, core.Request{Operation: "answer", Query: "research this"}, "r", "one")
	if r.Status != "succeeded" || r.Answer == "" || len(r.Attempts) != 6 || r.Results[0].ID != "S1" {
		t.Fatalf("rounds=%d result=%+v", rounds, r)
	}
	if len(db.checkpoints) < 3 {
		t.Fatal("evidence was not checkpointed")
	}
}
func TestAnswerRejectsInventedCitation(t *testing.T) {
	e, _, up := fixture()
	var n int
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		n++
		if n == 1 {
			return tool("search", `{"query":"q","provider":"exa"}`, "a"), nil
		}
		if n == 2 {
			return tool("finish", `{"summary":"covered","gaps":[]}`, "b"), nil
		}
		return core.Completion{Message: core.Message{Content: "Unsupported [S999]"}, Usage: core.Usage{CostKnown: true}}, nil
	}
	r := run(t, e, core.Request{Operation: "answer", Query: "q"}, "r", "one")
	if r.Answer != "" || r.Status != "partial" || len(r.Gaps) == 0 {
		t.Fatal(r)
	}
}
func TestAnswerBudgetReservationAndUnknownCost(t *testing.T) {
	e, _, up := fixture()
	e.cfg.Limits.DefaultRunMicros = 11000
	var n int
	up.complete = func(context.Context, core.Channel, []core.Message, []core.Tool, int) (core.Completion, error) {
		n++
		return tool("search", `{"query":"q","provider":"exa"}`, "a"), nil
	}
	up.call = func(context.Context, core.Channel, core.Call) (core.Output, error) {
		return core.Output{Sources: []core.Source{{URL: "https://example.org/a"}}}, nil
	}
	r := run(t, e, core.Request{Operation: "answer", Query: "q"}, "r", "one")
	if len(r.Attempts) > 4 || r.Status != "partial" {
		t.Fatalf("budget reserve failed: %+v", r)
	}
	if r.Usage.UnsettledAttempts != 1 || r.Usage.ReservedMicros != 1000 {
		t.Fatal("unknown charge vanished", r.Usage)
	}
}
func TestCancellationLeavesSentAttemptUncertain(t *testing.T) {
	e, db, up := fixture()
	started := make(chan struct{})
	up.call = func(ctx context.Context, _ core.Channel, _ core.Call) (core.Output, error) {
		close(started)
		<-ctx.Done()
		return core.Output{}, ctx.Err()
	}
	go func() { <-started; db.cancelled.Store(true) }()
	r := run(t, e, core.Request{Query: "q", Provider: "exa"}, "r", "one")
	if r.Status != "cancelled" || len(r.Attempts) != 1 || r.Attempts[0].Status != "uncertain" || r.Usage.UnsettledAttempts != 1 {
		t.Fatal(r)
	}
}
