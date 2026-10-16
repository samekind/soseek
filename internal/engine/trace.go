package engine

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jstar0/soseek/internal/core"
)

var credentialPattern = regexp.MustCompile(`(?i)(?:bearer\s+[^\s]+|(?:sk-|tvly-|fc-)[A-Za-z0-9_-]{12,})`)
var credentialAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|password|secret|authorization|access[_-]?token)\s*[:=]\s*[^\s&]+`)

func traceText(value string, limit int) string {
	value = credentialAssignmentPattern.ReplaceAllString(value, "$1=[REDACTED]")
	return clip(credentialPattern.ReplaceAllString(value, "[REDACTED]"), limit)
}
func traceURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	u.User = nil
	q := u.Query()
	for key := range q {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "authorization") {
			q.Set(key, "[REDACTED]")
		}
	}
	u.RawQuery = q.Encode()
	return traceText(u.String(), 2048)
}
func safeToolArguments(tool core.ToolCall) map[string]any {
	var input map[string]any
	if json.Unmarshal([]byte(tool.Function.Arguments), &input) != nil {
		return map[string]any{"invalid_json": true, "argument_bytes": len(tool.Function.Arguments)}
	}
	allowed := map[string][]string{"search": {"query", "provider", "providers"}, "read": {"url", "provider"}, "inspect": {"source_id", "query", "offset", "max_chars"}}[tool.Function.Name]
	out := map[string]any{}
	if tool.Function.Name == "finish" {
		if summary, ok := input["summary"].(string); ok {
			out["summary_chars"] = len([]rune(summary))
		}
		if gaps, ok := input["gaps"].([]any); ok {
			out["gap_count"] = len(gaps)
		}
		return out
	}
	for _, key := range allowed {
		value, ok := input[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if key == "url" {
				out[key] = traceURL(v)
			} else {
				out[key] = traceText(v, 1000)
			}
		case float64, bool:
			out[key] = v
		case []any:
			if key == "providers" {
				values := []string{}
				for _, item := range v {
					if text, ok := item.(string); ok {
						values = append(values, traceText(text, 64))
					}
				}
				out[key] = values
			}
		}
	}
	return out
}

func (s *execution) plannerState(round int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := max(int64(0), s.run.BudgetMicros-s.used-s.reserve)
	state := map[string]any{"round": round, "max_rounds": s.e.cfg.Limits.MaxRounds, "decision_rounds_after_this": max(0, s.e.cfg.Limits.MaxRounds-round), "upstream_calls_remaining": max(0, s.e.cfg.Limits.MaxCalls-s.calls), "pages_remaining": max(0, min(s.e.cfg.Limits.MaxPages, 6)-s.pages), "budget_available_micros": remaining, "final_answer_reserved_micros": s.reserve, "inspection_context_remaining_bytes": max(0, 24000-s.excerptBytes)}
	if !s.run.Deadline.IsZero() {
		state["deadline_remaining_seconds"] = max(0, int(time.Until(s.run.Deadline).Seconds()))
	}
	return state
}
func (s *execution) traceEvent(ctx context.Context, kind string, data any) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.e.db.AddEvent(persistCtx, s.run.ID, kind, data); err != nil {
		return core.E("checkpoint_failed", "planner trace could not be persisted; no subsequent tool will be sent")
	}
	return nil
}
func (s *execution) traceFailure(err error) {
	s.fail(err)
	s.mu.Lock()
	s.halt = true
	s.stop = "checkpoint_failed"
	s.mu.Unlock()
}
func toolFingerprint(tool core.ToolCall) string {
	var args any
	if json.Unmarshal([]byte(tool.Function.Arguments), &args) == nil {
		canonical, _ := json.Marshal(args)
		return tool.Function.Name + ":" + string(canonical)
	}
	return tool.Function.Name + ":" + tool.Function.Arguments
}

func toolResultTrace(round int, tool core.ToolCall, payload string, elapsed time.Duration) map[string]any {
	var envelope map[string]any
	_ = json.Unmarshal([]byte(payload), &envelope)
	out := map[string]any{"round": round, "tool_call_id": tool.ID, "tool": tool.Function.Name, "status": envelope["status"], "local_only": envelope["local_only"], "attempt_ids": envelope["attempt_ids"], "upstream_attempt_count": envelope["upstream_attempt_count"], "replayed": envelope["replayed"], "reused_attempt_ids": envelope["reused_attempt_ids"], "elapsed_ms": elapsed.Milliseconds()}
	errors := []map[string]any{}
	if items, ok := envelope["errors"].([]any); ok {
		for _, item := range items {
			if e, ok := item.(map[string]any); ok {
				summary := map[string]any{"code": e["code"], "retryable": e["retryable"], "uncertain": e["uncertain"], "retry_after_seconds": e["retry_after_seconds"]}
				if message, ok := e["message"].(string); ok {
					summary["message"] = traceText(message, 300)
				}
				errors = append(errors, summary)
			}
		}
	}
	out["errors"] = errors
	if result, ok := envelope["result"].(map[string]any); ok {
		if tool.Function.Name == "inspect" {
			summary := map[string]any{}
			for _, key := range []string{"source_id", "query_matched", "match_count", "matching", "matched_offsets", "start", "end", "next_offset", "next_match_offset", "more_matches", "has_more", "content_total_chars", "saved_for_final_answer"} {
				if value, exists := result[key]; exists {
					summary[key] = value
				}
			}
			if passages, ok := result["passages"].([]any); ok {
				ranges := []map[string]any{}
				for _, p := range passages {
					if passage, ok := p.(map[string]any); ok {
						ranges = append(ranges, map[string]any{"source_id": passage["source_id"], "start": passage["start"], "end": passage["end"]})
					}
				}
				summary["passages"] = ranges
			}
			out["inspection"] = summary
		}
		if sources, ok := result["untrusted_evidence"].([]any); ok {
			ids := []string{}
			for _, item := range sources {
				if source, ok := item.(map[string]any); ok {
					if id, ok := source["source_id"].(string); ok {
						ids = append(ids, id)
					}
				}
			}
			out["source_ids"] = ids
		}
	}
	return out
}
