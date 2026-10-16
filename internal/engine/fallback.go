package engine

import (
	"context"
	"strings"

	"github.com/jstar0/soseek/internal/core"
)

// providerChannels returns alternatives only within the selected provider. A
// pinned channel is an absolute constraint; health does not override it.
func (e *Engine) providerChannels(req core.Request, p core.Principal, op string, first core.Channel) []core.Channel {
	out := []core.Channel{first}
	if req.Channel != "" {
		return out
	}
	for _, c := range e.cfg.Channels {
		if c.Provider != first.Provider || c.ID == first.ID {
			continue
		}
		probe := req
		probe.Provider = first.Provider
		probe.Providers = nil
		probe.Channel = c.ID
		probe.Profile = "quick"
		if op == "search" && (req.Profile == "broad" || len(req.Providers) > 0 || req.Operation == "research" || req.Operation == "answer") {
			probe.Limit = min(probe.Limit, 8)
		}
		probe.Options = optionsFor(req.Options, first.Provider)
		channels, err := e.route(probe, p, op, false)
		if err == nil && len(channels) > 0 {
			out = append(out, channels[0])
		}
	}
	return out
}

func (s *execution) callProvider(ctx context.Context, req core.Request, first core.Channel, call core.Call, qid string, maxAttempts int) (core.Output, core.Channel, error, int) {
	channels := s.e.providerChannels(req, s.run.Principal, call.Operation, first)
	attempted := 0
	var last error
	selected := first
	for _, c := range channels {
		if maxAttempts > 0 && attempted >= maxAttempts {
			break
		}
		if reason := s.e.groupUnavailable(c); reason != "" {
			s.mu.Lock()
			s.warnings = append(s.warnings, c.ID+" excluded: "+reason)
			s.mu.Unlock()
			continue
		}
		if last != nil {
			s.mu.Lock()
			s.warnings = append(s.warnings, "Trying another channel of the same provider after "+asError(last).Error())
			s.mu.Unlock()
		}
		attempted++
		selected = c
		out, err := s.call(ctx, c, call, qid)
		if err == nil {
			return out, c, nil, attempted
		}
		last = err
		if req.Channel != "" || !fallbackAllowed(asError(err)) {
			return core.Output{}, c, err, attempted
		}
	}
	if last == nil {
		last = core.E("no_available_channel", "all permitted provider channels became unavailable before execution")
	}
	return core.Output{}, selected, last, attempted
}

func (e *Engine) routeExclusions(req core.Request, p core.Principal, op string) []string {
	names := req.Providers
	if req.Provider != "" {
		names = []string{req.Provider}
	}
	if len(names) == 0 {
		if op == "read" {
			names = e.cfg.ReadOrder
		} else {
			names = e.cfg.SearchOrder
		}
	}
	var notes []string
	for _, c := range e.cfg.Channels {
		if !has(names, c.Provider) || !p.Allows(c.Provider) || (req.Channel != "" && req.Channel != c.ID) {
			continue
		}
		reason := ""
		switch {
		case c.Disabled:
			reason = "disabled by configuration"
		case !has(c.Operations, op):
			reason = "operation not declared"
		case !e.implemented(c.Provider, op):
			reason = "operation not implemented"
		case e.groupUnavailable(c) != "":
			reason = e.groupUnavailable(c)
		case !optionsApply(req, c.Provider):
			reason = "provider-specific constraints require another provider"
		}
		if reason == "" {
			if _, err := costBound(c, op); err != nil {
				reason = "cost_bound_unavailable"
			}
		}
		if reason != "" {
			notes = append(notes, c.ID+" excluded: "+reason)
		}
	}
	return notes
}

func (s *execution) recordExclusions(req core.Request, op string) {
	notes := s.e.routeExclusions(req, s.run.Principal, op)
	s.mu.Lock()
	s.warnings = append(s.warnings, notes...)
	s.mu.Unlock()
}

func (e *Engine) fallbackPlanNotes(req core.Request, p core.Principal, op string, primary []core.Channel) []string {
	notes := e.routeExclusions(req, p, op)
	if req.Channel != "" {
		return notes
	}
	for _, first := range primary {
		channels := e.providerChannels(req, p, op, first)
		if len(channels) > 1 {
			ids := []string{}
			for _, c := range channels[1:] {
				ids = append(ids, c.ID)
			}
			notes = append(notes, first.Provider+" alternatives after a confirmed retryable, non-uncertain failure: "+strings.Join(ids, ", ")+"; all attempts share run limits")
		}
	}
	return notes
}
