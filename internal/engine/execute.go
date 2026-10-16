package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/fusion"
)

type execution struct {
	e                              *Engine
	run                            core.Run
	mu                             sync.Mutex
	sources                        []core.Source
	sourceIDs                      map[string]string
	contentSizes                   map[string]int
	readSources                    map[string]bool
	nextID                         int
	warnings, gaps                 []string
	routing                        []core.PlanStep
	queries                        []core.EvidenceQuery
	citations                      []core.Citation
	retrievalEvents                []core.RetrievalEvent
	excerpts                       []excerpt
	excerptBytes                   int
	cached                         []core.Attempt
	completedAttempts              []core.Attempt
	failures                       []core.Error
	used                           int64
	reserve                        int64
	calls, pages, chars, successes int
	errors                         int
	answer                         string
	raw                            json.RawMessage
	stop                           string
	halt                           bool
}

func (e *Engine) Execute(ctx context.Context, run core.Run) core.Result {
	if run.Principal.ID == "" {
		run.Principal.ID = run.PrincipalID
	}
	if run.PrincipalID == "" {
		run.PrincipalID = run.Principal.ID
	}
	if run.Plan.Request.Operation == "" {
		plan, err := e.Plan(run.Request, run.Principal)
		if err != nil {
			return core.Result{SchemaVersion: core.SchemaVersion, RunID: run.ID, Status: "failed", Operation: run.Request.Operation, Results: []core.Source{}, Attempts: []core.Attempt{}, Warnings: []string{err.Error()}, StopReason: "invalid_request"}
		}
		run.Plan = plan
	}
	run.Request = run.Plan.Request
	if run.BudgetMicros <= 0 {
		run.BudgetMicros, _ = core.ParseUSD(run.Plan.BudgetUSD)
	}
	deadline := run.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Duration(run.Request.DeadlineSeconds) * time.Second)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if e.db.Cancelled(ctx, run.ID) {
					cancel()
					return
				}
			}
		}
	}()
	s := &execution{e: e, run: run, sourceIDs: map[string]string{}}
	_ = e.db.AddEvent(ctx, run.ID, "execution_started", map[string]any{"operation": run.Request.Operation, "mode": run.Request.Mode})
	req := run.Request
	if req.Operation == "answer" {
		if c, err := e.planner(run.Principal); err == nil {
			s.reserve, _ = costBound(c, "planner")
		}
	}
	switch req.Operation {
	case "search":
		s.search(ctx, req, "query-1", false)
	case "read":
		s.read(ctx, req.URL, req, "read-1")
	case "research", "answer":
		if req.Mode == "agent" {
			s.agent(ctx)
		} else {
			s.pipeline(ctx)
		}
	default:
		op := strings.SplitN(req.Operation, ".", 2)
		if len(op) != 2 {
			s.fail(core.E("unsupported_operation", "invalid native operation"))
		} else {
			cs, err := e.route(req, run.Principal, op[1], false)
			if err != nil {
				s.fail(err)
			} else {
				s.recordExclusions(req, op[1])
				out, actual, err, _ := s.callProvider(ctx, req, cs[0], core.Call{Operation: op[1], Query: req.Query, URL: req.URL, Limit: req.Limit, Input: req.Input, Options: optionsFor(req.Options, cs[0].Provider)}, "native", 0)
				if err != nil {
					s.fail(err)
				} else {
					s.ingest(out, actual, "native")
					s.answer = out.Answer
					s.raw = out.Raw
				}
			}
		}
	}
	if req.Operation == "answer" && ctx.Err() == nil && len(s.sources) > 0 && !s.halt {
		s.synthesize(ctx)
	}
	result := s.result(ctx)
	if ctx.Err() != nil {
		if e.db.Cancelled(context.Background(), run.ID) {
			result.Status = "cancelled"
			result.StopReason = "cancelled"
		} else if len(result.Results) > 0 {
			result.Status = "partial"
			result.StopReason = "deadline_exceeded"
		} else {
			result.Status = "failed"
			result.StopReason = "deadline_exceeded"
		}
		result.Warnings = append(result.Warnings, "Execution stopped; already sent requests may still be charged.")
	}
	if result.StopReason == "" {
		if result.Status == "failed" || (result.Status == "partial" && s.errors > 0) {
			result.StopReason = "execution_failed"
		} else {
			result.StopReason = "completed"
		}
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer persistCancel()
	_ = e.db.Checkpoint(persistCtx, run.ID, result)
	return result
}

func (s *execution) result(ctx context.Context) core.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.run.Request.Limit
	if s.run.Request.Operation == "read" || strings.Contains(s.run.Request.Operation, ".") {
		limit = 0
	}
	sources := fusion.Merge(s.sources, limit, domainLimit(s.run.Request))
	if s.run.Request.Operation == "answer" || s.run.Request.Operation == "research" {
		sources = s.selectedSources()
	}
	sources = s.retainCitedSources(sources)
	if sources == nil {
		sources = []core.Source{}
	}
	r := core.Result{SchemaVersion: core.SchemaVersion, RunID: s.run.ID, Status: "succeeded", Operation: s.run.Request.Operation, Results: sources, Answer: s.answer, Routing: s.routing, Queries: s.queries, Citations: append([]core.Citation{}, s.citations...), RetrievalEvents: append([]core.RetrievalEvent{}, s.retrievalEvents...), Warnings: append([]string{}, s.warnings...), Gaps: append([]string{}, s.gaps...), StopReason: s.stop, Raw: s.raw}
	if s.errors > 0 {
		if len(sources) > 0 || s.raw != nil {
			r.Status = "partial"
		} else {
			r.Status = "failed"
		}
	}
	if len(s.gaps) > 0 || s.stop == "round_limit" || s.stop == "call_limit" || s.stop == "budget_exhausted" || s.stop == "no_progress" {
		if len(sources) > 0 {
			r.Status = "partial"
		} else {
			r.Status = "failed"
		}
	}
	if s.run.Request.Operation == "answer" && r.Answer == "" {
		if len(sources) > 0 {
			r.Status = "partial"
		} else {
			r.Status = "failed"
		}
		r.Warnings = append(r.Warnings, "No validated cited answer was produced.")
	}
	accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	attempts, err := s.e.db.Attempts(accountCtx, s.run.ID)
	if err != nil {
		r.Warnings = append(r.Warnings, "Attempt ledger could not be loaded; accounting is incomplete.")
		r.Status = "partial"
	}
	r.Attempts = append(attempts, s.cached...)
	if r.Attempts == nil {
		r.Attempts = []core.Attempt{}
	}
	for _, a := range r.Attempts {
		r.Usage.ReportedMicros += a.Usage.ReportedMicros
		r.Usage.EstimatedMicros += a.Usage.EstimatedMicros
		r.Usage.InputTokens += a.Usage.InputTokens
		r.Usage.OutputTokens += a.Usage.OutputTokens
		r.Usage.NativeCalls += a.Usage.NativeCalls
		r.Usage.Credits += a.Usage.Credits
		if !a.Cached && ((!a.Usage.CostKnown && a.Usage.EstimatedMicros == 0 && a.Usage.ReportedMicros == 0) || a.Status == "uncertain" || a.Status == "in_flight") {
			r.Usage.UnsettledAttempts++
			r.Usage.ReservedMicros += a.MaxMicros
		}
	}
	r.Usage.CostKnown = r.Usage.UnsettledAttempts == 0
	for _, source := range sources {
		if source.Truncated {
			r.Truncated = true
		}
	}
	return r
}

func (s *execution) call(ctx context.Context, c core.Channel, call core.Call, queryID string) (core.Output, error) {
	if err := ctx.Err(); err != nil {
		return core.Output{}, err
	}
	if s.e.db.Cancelled(ctx, s.run.ID) {
		return core.Output{}, context.Canceled
	}
	var boundErr error
	call, boundErr = s.e.boundedCall(c, call)
	if boundErr != nil {
		return core.Output{}, boundErr
	}
	if err := s.e.validateTargets(ctx, call); err != nil {
		return core.Output{}, err
	}
	if err := validateSensitiveFields(call.Options); err != nil {
		return core.Output{}, err
	}
	if err := validateSensitiveFields(call.Input); err != nil {
		return core.Output{}, err
	}
	if v, ok := s.e.up.(interface {
		Validate(core.Channel, core.Call) error
	}); ok {
		if err := v.Validate(c, call); err != nil {
			return core.Output{}, err
		}
	}
	cacheKey := s.cacheKey(c, call)
	cacheable := call.Operation == "search" || call.Operation == "read"
	if cacheable && !s.run.Request.Fresh && s.e.cfg.Limits.CacheSeconds > 0 {
		if out, err := s.e.db.CacheGet(ctx, s.run.PrincipalID, cacheKey); err == nil && out != nil {
			s.mu.Lock()
			s.cached = append(s.cached, core.Attempt{ID: fmt.Sprintf("cache-%d", len(s.cached)+1), RunID: s.run.ID, Provider: c.Provider, Channel: c.ID, Operation: call.Operation, Status: "succeeded", Cached: true, StartedAt: time.Now(), Usage: core.Usage{CostKnown: true}})
			s.successes++
			s.mu.Unlock()
			return *out, nil
		}
	}
	nativeBound := 0
	if c.Provider == "grok" && call.Operation == "search" {
		for _, m := range []map[string]any{call.Input, call.Options} {
			if v, ok := m["max_tool_calls"]; ok {
				switch n := v.(type) {
				case int:
					nativeBound = n
				case float64:
					nativeBound = int(n)
				}
			}
		}
	}
	a, release, err := s.begin(ctx, c, call.Operation, nativeBound)
	if err != nil {
		return core.Output{}, err
	}
	defer release()
	call.Progress = func(kind string, data any) error {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.e.db.AddEvent(persistCtx, s.run.ID, kind, map[string]any{"attempt_id": a.ID, "provider": c.Provider, "channel": c.ID, "detail": data})
	}
	callCtx, cancel := channelContext(ctx, c)
	defer cancel()
	out, callErr := s.e.up.Call(callCtx, c, call)
	s.end(a, out.Usage, callErr, c)
	if out.Usage.NativeCalls > 0 {
		s.mu.Lock()
		s.calls += int(out.Usage.NativeCalls) - nativeBound
		declared := 0
		for _, m := range []map[string]any{call.Input, call.Options} {
			if v, ok := m["max_tool_calls"]; ok {
				switch n := v.(type) {
				case int:
					declared = n
				case float64:
					declared = int(n)
				}
			}
		}
		if (declared > 0 && out.Usage.NativeCalls > int64(declared)) || s.calls > s.e.cfg.Limits.MaxCalls {
			s.halt = true
			s.errors++
			s.stop = "upstream_limit_exceeded"
			s.gaps = append(s.gaps, "Upstream reported native tool usage above the declared envelope; all subsequent upstream calls were stopped.")
			s.warnings = append(s.warnings, "The upstream exceeded its requested tool limit. Actual usage is retained; the local budget cannot undo charges already incurred.")
		}
		s.mu.Unlock()
	}
	if callErr == nil {
		s.mu.Lock()
		s.successes++
		s.mu.Unlock()
		if cacheable && s.e.cfg.Limits.CacheSeconds > 0 {
			cached := out
			cached.Usage = core.Usage{CostKnown: true}
			cached.Raw = nil
			_ = s.e.db.CachePut(ctx, s.run.PrincipalID, cacheKey, cached, time.Duration(s.e.cfg.Limits.CacheSeconds)*time.Second)
		}
	}
	return out, callErr
}
func (s *execution) begin(ctx context.Context, c core.Channel, op string, nativeBound int) (core.Attempt, func(), error) {
	bound, err := costBound(c, op)
	if err != nil {
		return core.Attempt{}, nil, err
	}
	s.mu.Lock()
	if s.halt {
		s.mu.Unlock()
		return core.Attempt{}, nil, core.E("upstream_limit_exceeded", "further calls stopped after upstream exceeded its declared envelope")
	}
	if op != "planner" && s.calls+1+nativeBound > s.e.cfg.Limits.MaxCalls {
		s.mu.Unlock()
		return core.Attempt{}, nil, core.E("call_limit", "maximum upstream tool calls reached")
	}
	if s.used+bound+s.reserve > s.run.BudgetMicros {
		s.mu.Unlock()
		return core.Attempt{}, nil, core.E("budget_exhausted", "declared call ceiling and reserved answer cost exceed remaining budget")
	}
	s.used += bound
	if op != "planner" {
		s.calls += 1 + nativeBound
	}
	s.mu.Unlock()
	rollback := func() {
		s.mu.Lock()
		s.used -= bound
		if op != "planner" {
			s.calls -= 1 + nativeBound
		}
		s.mu.Unlock()
	}
	release, err := s.e.acquire(ctx, c)
	if err != nil {
		rollback()
		return core.Attempt{}, nil, err
	}
	a := core.Attempt{RunID: s.run.ID, Provider: c.Provider, Channel: c.ID, Operation: op, Status: "in_flight", MaxMicros: bound, StartedAt: time.Now()}
	a, err = s.e.db.BeginAttempt(ctx, s.run.ID, a)
	if err != nil {
		release()
		rollback()
		return a, nil, err
	}
	s.mu.Lock()
	s.routing = append(s.routing, step(c, op, "executed within declared capability and budget"))
	s.mu.Unlock()
	_ = s.e.db.AddEvent(ctx, s.run.ID, "attempt_started", map[string]any{"attempt_id": a.ID, "provider": c.Provider, "channel": c.ID, "operation": op})
	return a, release, nil
}
func (s *execution) end(a core.Attempt, usage core.Usage, err error, c core.Channel) {
	now := time.Now()
	a.FinishedAt = &now
	a.Usage = usage
	a.Status = "succeeded"
	if err != nil {
		a.Error = asError(err)
		a.Status = "failed"
		if a.Error.Uncertain || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			a.Status = "uncertain"
			a.Error.Uncertain = true
		}
		s.e.cool(c, a.Error)
		if a.Error.Code == "upstream_unavailable" && !a.Error.Uncertain && a.Error.Retryable {
			a.Usage = core.Usage{CostKnown: true}
			usage = a.Usage
		}
		switch a.Error.Code {
		case "invalid_input", "unsupported_operation", "credential_error", "configuration_error":
			a.Usage = core.Usage{CostKnown: true}
			usage = a.Usage
		}
	}
	if (usage.CostKnown || usage.ReportedMicros > 0 || usage.EstimatedMicros > 0) && a.Status != "uncertain" {
		charged := usage.ReportedMicros + usage.EstimatedMicros
		s.mu.Lock()
		s.used += charged - a.MaxMicros
		if charged > a.MaxMicros {
			s.halt = true
			s.errors++
			s.stop = "cost_ceiling_exceeded"
			s.gaps = append(s.gaps, "Upstream usage exceeded the administrator-declared cost ceiling; no subsequent upstream calls are permitted.")
			s.warnings = append(s.warnings, "The configured cost ceiling was not a valid bound for this request; actual reported usage has been retained.")
		}
		s.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.e.db.EndAttempt(ctx, a); err != nil {
		s.mu.Lock()
		s.warnings = append(s.warnings, "Attempt settlement could not be persisted; its reservation remains uncertain.")
		s.errors++
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.completedAttempts = append(s.completedAttempts, a)
	s.mu.Unlock()
	_ = s.e.db.AddEvent(ctx, s.run.ID, "attempt_finished", map[string]any{"attempt_id": a.ID, "status": a.Status, "operation": a.Operation})
}
func (s *execution) cacheKey(c core.Channel, call core.Call) string {
	data, _ := json.Marshal(struct {
		Revision, Channel, Provider, Profile string
		Operation, Query, URL                string
		Limit                                int
		Options, Input                       map[string]any
	}{s.e.cfg.Revision, c.ID, c.Provider, s.run.Request.Profile, call.Operation, call.Query, call.URL, call.Limit, call.Options, call.Input})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func channelContext(ctx context.Context, c core.Channel) (context.Context, context.CancelFunc) {
	if c.TimeoutSeconds > 0 {
		return context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	}
	return context.WithCancel(ctx)
}
func asError(err error) *core.Error {
	var ce *core.Error
	if errors.As(err, &ce) {
		copy := *ce
		return &copy
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: "upstream_timeout", Message: "upstream deadline exceeded", Uncertain: true}
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: "cancelled", Message: "execution cancelled", Uncertain: true}
	}
	return core.E("execution_error", "operation failed")
}
func (s *execution) fail(err error) {
	if err == nil {
		return
	}
	ce := asError(err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors++
	s.failures = append(s.failures, *ce)
	s.warnings = append(s.warnings, ce.Error())
	if ce.Code == "budget_exhausted" || ce.Code == "call_limit" {
		s.stop = ce.Code
	}
}

func (s *execution) ingest(out core.Output, c core.Channel, qid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contentSizes == nil {
		s.contentSizes = map[string]int{}
	}
	// Native citation sources can be absent from a provider's ranked source
	// list. Keep them as unranked evidence rather than inventing SERP positions.
	known := map[string]bool{}
	for _, source := range out.Sources {
		known[fusion.CanonicalURL(source.URL)] = true
	}
	for _, citation := range out.Citations {
		if citation.URL == "" {
			continue
		}
		key := fusion.CanonicalURL(citation.URL)
		if !known[key] {
			out.Sources = append(out.Sources, core.Source{URL: citation.URL, Title: citation.Title, Occurrences: []core.Occurrence{{Provider: c.Provider, Channel: c.ID, QueryID: qid}}})
			known[key] = true
		}
	}
	for i, source := range out.Sources {
		source.Occurrences = append([]core.Occurrence{}, source.Occurrences...)
		source.FoundBy = append([]string{}, source.FoundBy...)
		if source.URL == "" {
			continue
		}
		key := fusion.CanonicalURL(source.URL)
		id := s.sourceIDs[key]
		if id == "" {
			s.nextID++
			id = fmt.Sprintf("S%d", s.nextID)
			s.sourceIDs[key] = id
		}
		source.ID = id
		if len(source.Occurrences) == 0 {
			rank := i + 1
			if c.Provider == "grok" || s.run.Request.Operation == "read" {
				rank = 0
			}
			source.Occurrences = []core.Occurrence{{Provider: c.Provider, Channel: c.ID, QueryID: qid, Rank: rank}}
		} else {
			for j := range source.Occurrences {
				if source.Occurrences[j].Provider == "" {
					source.Occurrences[j].Provider = c.Provider
				}
				if source.Occurrences[j].Channel == "" {
					source.Occurrences[j].Channel = c.ID
				}
				source.Occurrences[j].QueryID = qid
			}
		}
		if !has(source.FoundBy, c.Provider) {
			source.FoundBy = append(source.FoundBy, c.Provider)
		}
		existingSize := s.contentSizes[key]
		available := existingSize + max(0, s.e.cfg.Limits.MaxChars-s.chars)
		if len(source.Content) > available {
			if available < 0 {
				available = 0
			}
			source.Content = source.Content[:available]
			for !utf8.ValidString(source.Content) && len(source.Content) > 0 {
				source.Content = source.Content[:len(source.Content)-1]
			}
			source.Truncated = true
		}
		if len(source.Content) > existingSize {
			s.chars += len(source.Content) - existingSize
			s.contentSizes[key] = len(source.Content)
		}
		if source.Content != "" && source.FetchedAt.IsZero() {
			source.FetchedAt = time.Now().UTC()
		}
		s.sources = append(s.sources, source)
	}
	for _, citation := range out.Citations {
		if citation.URL == "" {
			continue
		}
		citation.SourceID = s.sourceIDs[fusion.CanonicalURL(citation.URL)]
		if citation.AnswerText == "" {
			citation.AnswerText = out.Answer
		}
		duplicate := false
		for _, existing := range s.citations {
			if sameCitation(existing, citation) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			s.citations = append(s.citations, citation)
		}
	}
	s.retrievalEvents = append(s.retrievalEvents, out.RetrievalEvents...)
	s.warnings = append(s.warnings, out.Warnings...)
}

func sameCitation(a, b core.Citation) bool {
	indexEqual := func(x, y *int) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return *x == *y
	}
	return a.SourceID == b.SourceID && a.URL == b.URL && a.Title == b.Title && a.AnswerText == b.AnswerText && a.OffsetUnit == b.OffsetUnit && indexEqual(a.StartIndex, b.StartIndex) && indexEqual(a.EndIndex, b.EndIndex)
}

func (s *execution) search(ctx context.Context, req core.Request, qid string, broad bool) []core.Source {
	s.recordExclusions(req, "search")
	cs, err := s.e.route(req, s.run.Principal, "search", broad)
	if err != nil {
		s.fail(err)
		return nil
	}
	parallel := broad || len(req.Providers) > 0 || req.Profile == "broad"
	providers := []string{}
	for _, c := range cs {
		providers = append(providers, c.Provider)
	}
	s.mu.Lock()
	s.queries = append(s.queries, core.EvidenceQuery{ID: qid, Query: req.Query, Providers: providers})
	s.mu.Unlock()
	var resultMu sync.Mutex
	var found []core.Source
	invoke := func(c core.Channel, maxAttempts int) (error, int) {
		limit := req.Limit
		if parallel {
			limit = min(limit, 8)
		}
		out, actual, err, attempts := s.callProvider(ctx, req, c, core.Call{Operation: "search", Query: req.Query, Limit: limit, Options: optionsFor(req.Options, c.Provider)}, qid, maxAttempts)
		if err != nil {
			return err, attempts
		}
		s.ingest(out, actual, qid)
		resultMu.Lock()
		found = append(found, out.Sources...)
		resultMu.Unlock()
		if len(cs) == 1 && out.Answer != "" && s.run.Request.Operation == "search" {
			s.mu.Lock()
			s.answer = out.Answer
			s.raw = out.Raw
			s.mu.Unlock()
		}
		return nil, attempts
	}
	if parallel {
		var wg sync.WaitGroup
		for _, c := range cs {
			wg.Add(1)
			go func(c core.Channel) {
				defer wg.Done()
				if err, _ := invoke(c, 0); err != nil {
					s.fail(err)
				}
			}(c)
		}
		wg.Wait()
	} else {
		remaining := 2
		for i, c := range cs {
			err, attempts := invoke(c, remaining)
			remaining -= attempts
			if err == nil {
				break
			}
			s.fail(err)
			ce := asError(err)
			if remaining <= 0 || i == len(cs)-1 || req.Provider != "" || !fallbackAllowed(ce) {
				break
			}
		}
	}
	return found
}
func fallbackAllowed(e *core.Error) bool {
	return e.Retryable && !e.Uncertain && e.RetryAfterSeconds == 0 && e.Code != "rate_limited" && e.Code != "upstream_rate_limited" && e.Code != "http_429" && e.Code != "unauthorized" && e.Code != "forbidden" && e.Code != "quota_exceeded"
}
func (s *execution) read(ctx context.Context, raw string, req core.Request, qid string) {
	req.URL = raw
	s.recordExclusions(req, "read")
	s.mu.Lock()
	if s.pages >= min(s.e.cfg.Limits.MaxPages, 6) {
		s.mu.Unlock()
		s.fail(core.E("page_limit", "maximum pages reached"))
		return
	}
	s.pages++
	s.mu.Unlock()
	cs, err := s.e.route(req, s.run.Principal, "read", false)
	if err != nil {
		s.fail(err)
		return
	}
	out, actual, err, _ := s.callProvider(ctx, req, cs[0], core.Call{Operation: "read", URL: raw, Options: optionsFor(req.Options, cs[0].Provider)}, qid, 0)
	if err != nil {
		s.fail(err)
		return
	}
	for i := range out.Sources {
		if out.Sources[i].URL == "" {
			out.Sources[i].URL = raw
		}
		if len(out.Sources[i].Occurrences) == 0 {
			out.Sources[i].Occurrences = []core.Occurrence{{Provider: actual.Provider, Channel: actual.ID, QueryID: qid}}
		}
	}
	s.ingest(out, actual, qid)
	s.mu.Lock()
	if s.readSources == nil {
		s.readSources = map[string]bool{}
	}
	for _, source := range out.Sources {
		if id := s.sourceIDs[fusion.CanonicalURL(source.URL)]; id != "" {
			s.readSources[id] = true
		}
	}
	s.mu.Unlock()
}
func (s *execution) pipeline(ctx context.Context) {
	req := s.run.Request
	s.search(ctx, req, "query-1", req.Provider == "" && len(req.Providers) == 0)
	selected := fusion.Merge(s.sources, min(s.e.cfg.Limits.MaxPages, 6), domainLimit(req))
	for _, source := range selected {
		if ctx.Err() != nil {
			break
		}
		s.read(ctx, source.URL, core.Request{}, "read-"+source.ID)
		s.checkpoint(ctx)
	}
	if len(selected) == 0 {
		s.mu.Lock()
		s.gaps = append(s.gaps, "No sources were found to read.")
		s.mu.Unlock()
	}
}
func (s *execution) checkpoint(ctx context.Context) {
	r := s.result(ctx)
	_ = s.e.db.Checkpoint(ctx, s.run.ID, r)
}
func optionsFor(options map[string]any, provider string) map[string]any {
	out := map[string]any{}
	for k, v := range options {
		parts := strings.SplitN(k, ".", 2)
		if len(parts) == 1 {
			out[k] = v
		} else if parts[0] == provider {
			out[parts[1]] = v
		}
	}
	return out
}
func domainLimit(req core.Request) int {
	for k := range req.Options {
		l := strings.ToLower(strings.ReplaceAll(k, "_", ""))
		if strings.HasSuffix(l, "includedomains") || strings.HasSuffix(l, "alloweddomains") {
			return 0
		}
	}
	if req.Operation == "read" {
		return 0
	}
	return 2
}
