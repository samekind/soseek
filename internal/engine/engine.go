// Package engine executes bounded, auditable searches and model-led research.
package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/samekind/soseek/internal/core"
	"github.com/samekind/soseek/internal/provider"
)

type Engine struct {
	cfg     core.Config
	db      core.ExecutionStore
	up      core.ProviderClient
	mu      sync.Mutex
	groups  map[string]*quota
	resolve resolver
}

func New(cfg core.Config, db core.ExecutionStore, up core.ProviderClient) *Engine {
	if cfg.Limits.MaxCalls <= 0 {
		cfg.Limits.MaxCalls = 12
	}
	if cfg.Limits.MaxRounds <= 0 {
		cfg.Limits.MaxRounds = 6
	}
	if cfg.Limits.MaxPages <= 0 {
		cfg.Limits.MaxPages = 6
	}
	if cfg.Limits.MaxChars <= 0 {
		cfg.Limits.MaxChars = 60000
	}
	if cfg.Limits.MaxDeadline <= 0 {
		cfg.Limits.MaxDeadline = 180
	}
	if cfg.Limits.DefaultRunMicros <= 0 {
		cfg.Limits.DefaultRunMicros = 250000
	}
	if cfg.Limits.MaxRunMicros <= 0 {
		cfg.Limits.MaxRunMicros = 1000000
	}
	e := &Engine{cfg: cfg, db: db, up: up, groups: map[string]*quota{}, resolve: defaultResolve}
	for _, c := range cfg.Channels {
		g := c.QuotaGroup
		if g == "" {
			g = c.ID
		}
		q := e.groups[g]
		if q == nil {
			q = &quota{rpm: c.RPM, concurrency: c.Concurrency}
			e.groups[g] = q
		} else {
			q.rpm = minPositive(q.rpm, c.RPM)
			q.concurrency = minPositive(q.concurrency, c.Concurrency)
		}
	}
	return e
}

func (e *Engine) Plan(req core.Request, p core.Principal) (core.Plan, error) {
	plan := core.Plan{SchemaVersion: core.SchemaVersion, Revision: e.cfg.Revision}
	if req.Operation == "" {
		req.Operation = "search"
	}
	if req.Provider != "" && len(req.Providers) > 0 {
		return plan, core.E("invalid_request", "provider and providers are mutually exclusive")
	}
	if req.Channel != "" && req.Provider == "" {
		return plan, core.E("invalid_request", "channel requires an explicit provider")
	}
	if len(req.Providers) > 3 {
		return plan, core.E("invalid_request", "at most three providers can be combined")
	}
	seen := map[string]bool{}
	for _, v := range req.Providers {
		if seen[v] || v == "" {
			return plan, core.E("invalid_request", "providers must be distinct and nonempty")
		}
		seen[v] = true
	}
	if req.Intent == "" {
		req.Intent = "general"
	}
	if req.Intent != "general" {
		return plan, core.E("unsupported_intent", "only the configured general routing policy is available; use a registered native operation for specialized search")
	}
	switch req.Operation {
	case "search", "read", "research", "answer":
	case "call":
		if req.Provider == "" {
			return plan, core.E("invalid_request", "native call requires provider.operation")
		}
	default:
		if parts := strings.SplitN(req.Operation, ".", 2); len(parts) == 2 {
			if req.Provider != "" && req.Provider != parts[0] {
				return plan, core.E("invalid_request", "operation provider conflicts with provider")
			}
			req.Provider = parts[0]
		} else {
			return plan, core.E("unsupported_operation", "unknown operation")
		}
	}
	if req.Operation == "call" {
		return plan, core.E("invalid_request", "use provider.operation as operation for native calls")
	}
	if !strings.Contains(req.Operation, ".") && len(req.Input) > 0 {
		return plan, core.E("invalid_request", "input is reserved for registered provider.operation calls; use options for standard search/read")
	}
	if (req.Operation == "search" || req.Operation == "research" || req.Operation == "answer") && strings.TrimSpace(req.Query) == "" {
		return plan, core.E("invalid_request", "query is required")
	}
	if req.Operation == "read" && req.URL == "" {
		return plan, core.E("invalid_request", "URL is required")
	}
	if len(req.Query) > 16000 {
		return plan, core.E("invalid_request", "query exceeds 16000 characters")
	}
	if req.Limit < 0 || req.Limit > 50 {
		return plan, core.E("invalid_request", "limit must be between 1 and 50")
	}
	research := req.Operation == "research" || req.Operation == "answer"
	if req.Profile == "" {
		if research {
			req.Profile = "deep"
		} else {
			req.Profile = "quick"
		}
	}
	if req.Profile != "quick" && req.Profile != "broad" && req.Profile != "deep" {
		return plan, core.E("invalid_profile", "supported profiles: quick, broad, deep")
	}
	if req.Profile == "deep" && !research {
		return plan, core.E("invalid_profile", "deep applies only to research and answer")
	}
	if req.Profile == "broad" && req.Operation != "search" && !research {
		return plan, core.E("invalid_profile", "broad applies only to search")
	}
	if !research && req.Mode != "" {
		return plan, core.E("invalid_request", "mode applies only to research and answer")
	}
	if research {
		if req.Mode == "" {
			req.Mode = "agent"
		}
		if req.Mode != "agent" && req.Mode != "pipeline" {
			return plan, core.E("invalid_request", "mode must be agent or pipeline")
		}
	}
	if len(req.Providers) > 0 && req.Profile == "quick" {
		req.Profile = "broad"
	}
	if req.Provider != "" && req.Profile == "broad" {
		return plan, core.E("invalid_request", "explicit provider cannot be combined with broad")
	}
	if req.Limit == 0 && !strings.Contains(req.Operation, ".") {
		if req.Profile == "broad" || research {
			req.Limit = 12
		} else {
			req.Limit = 8
		}
	}
	deadline := 20
	if req.Profile == "broad" {
		deadline = 30
	}
	if research {
		deadline = 180
	}
	if research && req.Mode == "pipeline" {
		deadline = 120
	}
	if req.DeadlineSeconds < 0 {
		return plan, core.E("invalid_request", "deadline must be positive")
	}
	if req.DeadlineSeconds == 0 {
		req.DeadlineSeconds = min(deadline, e.cfg.Limits.MaxDeadline)
	}
	if req.DeadlineSeconds > e.cfg.Limits.MaxDeadline {
		return plan, core.E("deadline_exceeded_limit", "deadline exceeds deployment limit")
	}
	budget := e.cfg.Limits.DefaultRunMicros
	if req.BudgetUSD != "" {
		v, err := core.ParseUSD(req.BudgetUSD)
		if err != nil || v <= 0 {
			return plan, core.E("invalid_budget", "budget_usd must be a positive decimal")
		}
		budget = v
	}
	if budget > e.cfg.Limits.MaxRunMicros {
		return plan, core.E("budget_exceeded_limit", "budget exceeds deployment limit")
	}
	if p.DailyMicros > 0 && budget > p.DailyMicros {
		return plan, core.E("budget_exceeded_limit", "run budget exceeds principal daily budget")
	}
	req.BudgetUSD = core.USD(budget)
	plan.Request = req
	plan.BudgetUSD = req.BudgetUSD
	if err := validateSensitiveFields(req.Options); err != nil {
		return plan, err
	}
	if err := validateSensitiveFields(req.Input); err != nil {
		return plan, err
	}
	if err := e.validateOptionNamespaces(req); err != nil {
		return plan, err
	}
	if research && req.Mode == "agent" {
		c, err := e.planner(p)
		if err != nil {
			return plan, err
		}
		if req.Provider != "" || len(req.Providers) > 0 || req.Channel != "" {
			return plan, core.E("invalid_request", "agent chooses providers; use pipeline for a fixed provider selection")
		}
		bound, _ := costBound(c, "planner")
		minimum := bound
		if req.Operation == "answer" {
			minimum *= 2
		}
		if minimum > budget {
			return plan, core.E("budget_exhausted", "budget cannot cover initial planning and any required final answer reservation")
		}
		plan.Dynamic = true
		plan.Steps = []core.PlanStep{step(c, "planner", "Grok selects registered tools under run limits")}
		plan.MaxCostUSD = core.USD(budget)
		plan.Warnings = []string{"Dynamic plan: actual tool selection is recorded during execution; configured cost ceilings are not a quote."}
		return plan, nil
	}
	op := req.Operation
	if research {
		op = "search"
	}
	if strings.Contains(op, ".") {
		op = strings.SplitN(op, ".", 2)[1]
	}
	channels, err := e.route(req, p, op, research)
	if err != nil {
		return plan, err
	}
	plan.Warnings = append(plan.Warnings, e.fallbackPlanNotes(req, p, op, channels)...)
	var maxCost int64
	for i, c := range channels {
		reason := "explicit provider/channel"
		if req.Provider == "" {
			reason = "general configured_priority policy in deployment order"
		}
		if len(channels) > 1 && req.Profile == "quick" && i > 0 {
			reason = "fallback only after a confirmed retryable, non-uncertain failure"
		}
		plan.Steps = append(plan.Steps, step(c, op, reason))
		b, _ := costBound(c, op)
		maxCost += b
	}
	// Alternative channels are possible chargeable attempts, not free retries.
	// Include them in the plan ceiling even though only the primary is a step.
	maxCost = 0
	remainingAttempts := e.cfg.Limits.MaxCalls
	if op == "search" && req.Profile == "quick" && len(req.Providers) == 0 {
		remainingAttempts = min(remainingAttempts, 2)
	}
	for _, c := range channels {
		for _, candidate := range e.providerChannels(req, p, op, c) {
			if remainingAttempts <= 0 {
				break
			}
			b, _ := costBound(candidate, op)
			maxCost += b
			remainingAttempts--
		}
	}
	if research {
		plan.Dynamic = true
		maxCost = budget
		plan.Warnings = append(plan.Warnings, "Pipeline selects at most six public pages from ranked evidence; no model replanning.")
	}
	if req.Operation == "answer" {
		c, err := e.planner(p)
		if err != nil {
			return plan, err
		}
		plan.Steps = append(plan.Steps, step(c, "planner", "synthesize answer from gathered evidence without tools"))
	}
	if !research && len(channels) > 0 {
		b, _ := costBound(channels[0], op)
		if b > budget {
			return plan, core.E("budget_exhausted", "budget cannot cover the selected operation")
		}
	}
	if maxCost > budget {
		plan.Warnings = append(plan.Warnings, "Budget is lower than all planned ceilings; later attempts can be skipped.")
		maxCost = budget
	}
	plan.MaxCostUSD = core.USD(maxCost)
	return plan, nil
}

func (e *Engine) route(req core.Request, p core.Principal, op string, broad bool) ([]core.Channel, error) {
	providers := req.Providers
	if req.Provider != "" {
		providers = []string{req.Provider}
	}
	if len(providers) == 0 {
		if op == "read" {
			providers = e.cfg.ReadOrder
		} else {
			providers = e.cfg.SearchOrder
		}
		if len(providers) == 0 {
			return nil, core.E("no_available_channel", "no routing order is configured")
		}
	}
	limit := 1
	if len(req.Providers) > 0 || req.Profile == "broad" || broad {
		limit = 3
	} else if req.Provider == "" && op == "search" {
		limit = 2
	}
	out := []core.Channel{}
	used := map[string]bool{}
	var rejected []string
	for _, name := range providers {
		if len(out) >= limit {
			break
		}
		if used[name] {
			continue
		}
		used[name] = true
		if !p.Allows(name) {
			if req.Provider != "" || len(req.Providers) > 0 {
				return nil, core.E("forbidden", "provider is not allowed")
			}
			rejected = append(rejected, name+": forbidden")
			continue
		}
		var found *core.Channel
		for _, c := range e.cfg.Channels {
			if c.Provider != name || (req.Channel != "" && req.Channel != c.ID) {
				continue
			}
			if c.Disabled {
				rejected = append(rejected, c.ID+": disabled by configuration")
				continue
			}
			if reason := e.credentialUnavailable(c); reason != "" {
				rejected = append(rejected, c.ID+": "+reason)
				continue
			}
			if !has(c.Operations, op) {
				rejected = append(rejected, c.ID+": operation not declared")
				continue
			}
			if !e.implemented(name, op) {
				continue
			}
			if reason := e.groupUnavailable(c); reason != "" {
				rejected = append(rejected, c.ID+": "+reason)
				continue
			}
			if _, err := costBound(c, op); err != nil {
				rejected = append(rejected, c.ID+": cost_bound_unavailable")
				continue
			}
			if !optionsApply(req, name) {
				rejected = append(rejected, c.ID+": provider-specific constraints require another provider")
				continue
			}
			if validator, ok := e.up.(interface {
				Validate(core.Channel, core.Call) error
			}); ok {
				limit := req.Limit
				if op == "search" && (broad || req.Profile == "broad" || len(req.Providers) > 0) {
					limit = min(limit, 8)
				}
				call := core.Call{Operation: op, Query: req.Query, URL: req.URL, Limit: limit, Options: optionsFor(req.Options, name), Input: req.Input}
				if !strings.Contains(req.Operation, ".") && op == "search" {
					call = provider.StandardSearchCall(c, call)
				}
				var boundErr error
				call, boundErr = e.boundedCall(c, call)
				if boundErr != nil {
					return nil, boundErr
				}
				if err := validator.Validate(c, call); err != nil {
					if req.Provider != "" || len(req.Providers) > 0 {
						return nil, err
					}
					rejected = append(rejected, c.ID+": input constraints unsupported")
					continue
				}
			}
			cc := c
			found = &cc
			break
		}
		if found == nil {
			if req.Provider != "" || len(req.Providers) > 0 {
				return nil, core.E("no_available_channel", "no permitted, declared channel with a cost bound for "+name+"."+op+"; "+strings.Join(rejected, ", "))
			}
			continue
		}
		out = append(out, *found)
	}
	if len(out) == 0 {
		return nil, core.E("no_available_channel", "no channel satisfies operation, permission and cost bounds; "+strings.Join(rejected, ", "))
	}
	return out, nil
}
func (e *Engine) implemented(provider, op string) bool {
	for _, o := range e.up.Operations(provider) {
		if o.ID == op || o.ID == provider+"."+op {
			return true
		}
	}
	return false
}
func (e *Engine) planner(p core.Principal) (core.Channel, error) {
	for _, c := range e.cfg.Channels {
		if c.ID == e.cfg.PlannerChannel && !c.Disabled && c.Provider == "grok" && p.Allows(c.Provider) && has(c.Operations, "planner") {
			if reason := e.credentialUnavailable(c); reason != "" {
				return c, core.E("planner_unavailable", "Grok planner channel excluded: "+reason)
			}
			if reason := e.groupUnavailable(c); reason != "" {
				return c, core.E("planner_unavailable", "Grok planner channel excluded: "+reason)
			}
			if _, err := costBound(c, "planner"); err != nil {
				return c, err
			}
			return c, nil
		}
	}
	return core.Channel{}, core.E("planner_unavailable", "Grok planner is not configured or permitted; pipeline must be explicitly requested")
}

// Resolve availability on every decision so vault disable/restore takes effect
// without restarting the engine. Legacy clients require no extra key read.
func (e *Engine) credentialUnavailable(c core.Channel) string {
	if checker, ok := e.up.(interface{ CredentialAvailable(core.Channel) bool }); ok && !checker.CredentialAvailable(c) {
		return "referenced credential is unavailable or disabled"
	}
	return ""
}
func costBound(c core.Channel, op string) (int64, error) {
	v, ok := c.CostBounds[op]
	if !ok || v <= 0 {
		return 0, core.E("cost_bound_unavailable", "no declared cost ceiling for "+c.ID+"."+op)
	}
	return v, nil
}
func step(c core.Channel, op, reason string) core.PlanStep {
	v, _ := costBound(c, op)
	return core.PlanStep{Provider: c.Provider, Channel: c.ID, Operation: op, Reason: reason, MaxCostUSD: core.USD(v)}
}
func has(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

func (e *Engine) Capabilities(p core.Principal) any {
	cs := []map[string]any{}
	providers := map[string][]core.Operation{}
	for _, c := range e.cfg.Channels {
		if !p.Allows(c.Provider) {
			continue
		}
		providers[c.Provider] = e.up.Operations(c.Provider)
		ops := []string{}
		for _, op := range c.Operations {
			if e.implemented(c.Provider, op) {
				ops = append(ops, op)
			}
		}
		reason := e.groupUnavailable(c)
		if credentialReason := e.credentialUnavailable(c); credentialReason != "" {
			reason = credentialReason
		}
		cs = append(cs, map[string]any{"id": c.ID, "provider": c.Provider, "declared_operations": ops, "cost_bounds_micros": c.CostBounds, "verified": c.Verified, "available": !c.Disabled && reason == "", "unavailable_reason": reason, "model": c.Model})
	}
	_, pe := e.planner(p)
	return map[string]any{"schema_version": core.SchemaVersion, "version": core.Version, "revision": e.cfg.Revision, "providers": providers, "channels": cs, "intents": []string{"general"}, "profiles": map[string]any{"quick": map[string]any{"operations": []string{"search", "read"}, "max_search_attempts": 2}, "broad": map[string]any{"operations": []string{"search"}, "max_providers": 3}, "deep": map[string]any{"operations": []string{"research", "answer"}, "default_mode": "agent", "planner_available": pe == nil}}, "limits": e.cfg.Limits, "routing": map[string]any{"policy": "configured_priority", "search_order": allowedOrder(e.cfg.SearchOrder, p), "read_order": allowedOrder(e.cfg.ReadOrder, p)}}
}
func allowedOrder(ss []string, p core.Principal) []string {
	out := []string{}
	for _, v := range ss {
		if p.Allows(v) {
			out = append(out, v)
		}
	}
	return out
}
func validateSensitiveFields(m map[string]any) error {
	for k, v := range m {
		key := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
		switch key {
		case "api_key", "apikey", "authorization", "headers", "base_url", "baseurl", "endpoint", "key", "token", "password", "method":
			return core.E("invalid_parameter", fmt.Sprintf("%s cannot override transport or authentication", k))
		}
		if nested, ok := v.(map[string]any); ok && key != "schema" {
			if err := validateSensitiveFields(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) validateOptionNamespaces(req core.Request) error {
	for key := range req.Options {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 1 {
			continue
		}
		found := false
		for _, c := range e.cfg.Channels {
			if c.Provider == parts[0] {
				found = true
				break
			}
		}
		if !found || parts[1] == "" {
			return core.E("invalid_parameter", "unknown provider option namespace: "+parts[0])
		}
		if req.Provider != "" && parts[0] != req.Provider {
			return core.E("invalid_parameter", "provider option does not match the explicitly selected provider")
		}
		if len(req.Providers) > 0 && !has(req.Providers, parts[0]) {
			return core.E("invalid_parameter", "provider option does not match the explicitly selected provider set")
		}
	}
	return nil
}
func optionsApply(req core.Request, provider string) bool {
	if len(req.Providers) > 0 {
		return true
	}
	for key := range req.Options {
		if parts := strings.SplitN(key, ".", 2); len(parts) == 2 && parts[0] != provider {
			return false
		}
	}
	return true
}
