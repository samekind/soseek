package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samekind/soseek/internal/core"
)

// boundedCall constrains work-amplifying native parameters before cost
// reservation. Administrator ceilings must cover this advertised envelope;
// merely knowing a vendor's base price is not a safe upper bound.
func (e *Engine) boundedCall(c core.Channel, call core.Call) (core.Call, error) {
	copyMap := func(m map[string]any) map[string]any {
		b, _ := json.Marshal(m)
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		if out == nil {
			out = map[string]any{}
		}
		return out
	}
	call.Input = copyMap(call.Input)
	call.Options = copyMap(call.Options)
	pageLimit := min(e.cfg.Limits.MaxPages, 6)
	lookup := func(name string) (any, bool) {
		for _, m := range []map[string]any{call.Input, call.Options} {
			for key, v := range m {
				if strings.TrimPrefix(key, c.Provider+".") == name {
					return v, true
				}
			}
		}
		return nil, false
	}
	setDefault := func(name string, value any) {
		if _, ok := lookup(name); !ok {
			call.Input[name] = value
		}
	}
	op := strings.TrimPrefix(call.Operation, c.Provider+".")
	if op == "crawl" {
		if c.Provider == "tavily" {
			setDefault("allow_external", false)
		}
		if c.Provider == "firecrawl" {
			setDefault("allowExternalLinks", false)
		}
		for _, name := range []string{"allow_external", "allowExternalLinks"} {
			if v, ok := lookup(name); ok && v == true {
				return call, core.E("unsafe_crawl_scope", "cross-domain crawl traversal is disabled; submit separately validated URLs instead")
			}
		}
		if call.Limit > 0 && call.Limit > pageLimit {
			return call, core.E("work_limit", "crawl limit exceeds deployment page limit")
		}
		setDefault("limit", pageLimit)
	}
	if op == "map" && call.Limit == 0 {
		setDefault("limit", 50)
	}
	maxNumber := func(name string, maxValue int) error {
		v, ok := lookup(name)
		if !ok {
			return nil
		}
		n, ok := v.(float64)
		if !ok {
			return nil
		}
		if n > float64(maxValue) {
			return core.E("work_limit", fmt.Sprintf("%s exceeds the deployment limit of %d", name, maxValue))
		}
		return nil
	}
	for _, name := range []string{"numResults", "max_results", "num"} {
		if err := maxNumber(name, 50); err != nil {
			return call, err
		}
	}
	if op == "crawl" {
		for _, name := range []string{"limit", "max_breadth", "maxConcurrency"} {
			if err := maxNumber(name, pageLimit); err != nil {
				return call, err
			}
		}
		for _, name := range []string{"max_depth", "maxDiscoveryDepth"} {
			if err := maxNumber(name, 3); err != nil {
				return call, err
			}
		}
	}
	if op == "map" {
		if err := maxNumber("limit", 50); err != nil {
			return call, err
		}
	}
	basePages := 1
	for _, name := range []string{"ids", "urls"} {
		if v, ok := lookup(name); ok {
			if list, ok := v.([]any); ok {
				if len(list) > pageLimit {
					return call, core.E("work_limit", "URL list exceeds deployment page limit")
				}
				basePages = max(1, len(list))
			}
		}
	}
	if err := maxNumber("subpages", max(0, pageLimit/basePages-1)); err != nil {
		return call, err
	}
	if c.Provider == "grok" && op == "search" {
		if e.cfg.Limits.MaxCalls < 2 {
			return call, core.E("call_limit", "native search requires budget for the request and at least one native tool call")
		}
		setDefault("max_tool_calls", min(2, e.cfg.Limits.MaxCalls-1))
		setDefault("max_output_tokens", 2048)
		if err := maxNumber("max_tool_calls", min(e.cfg.Limits.MaxCalls, 3)); err != nil {
			return call, err
		}
		if err := maxNumber("max_output_tokens", 4096); err != nil {
			return call, err
		}
	}
	for _, name := range []string{"query", "q"} {
		if v, ok := lookup(name); ok {
			if text, ok := v.(string); ok && len(text) > 16000 {
				return call, core.E("work_limit", "query exceeds 16000 bytes")
			}
		}
	}
	return call, nil
}
