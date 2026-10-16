package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/samekind/soseek/internal/core"
)

// Complete sends exactly the persisted conversation supplied by the executor.
// It does not execute, retry, or synthesize any tool call on the model's behalf.
func (c *Client) Complete(ctx context.Context, ch core.Channel, messages []core.Message, tools []core.Tool, maxTokens int) (core.Completion, error) {
	if ch.Provider != "grok" {
		return core.Completion{}, core.E("unsupported_operation", "planner completion requires a Grok channel")
	}
	if ch.Model == "" {
		return core.Completion{}, core.E("configuration_error", "Grok channel requires a model")
	}
	if len(messages) == 0 {
		return core.Completion{}, core.E("invalid_input", "planner conversation is empty")
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	if maxTokens > 32768 {
		return core.Completion{}, core.E("invalid_input", "planner max_tokens exceeds 32768")
	}
	key, err := readKey(ch)
	if err != nil {
		return core.Completion{}, err
	}
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := map[string]any{"model": ch.Model, "messages": messages, "max_tokens": maxTokens, "stream": false}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = true
	}
	m, _, err := c.request(ctx, ch, key, http.MethodPost, "chat/completions", body)
	if err != nil {
		return core.Completion{}, err
	}
	choices := array(m["choices"])
	if len(choices) == 0 {
		return core.Completion{}, &core.Error{Code: "upstream_protocol_error", Message: "planner response has no completion choice", Uncertain: true}
	}
	choice := object(choices[0])
	message := object(choice["message"])
	if message == nil {
		return core.Completion{}, &core.Error{Code: "upstream_protocol_error", Message: "planner response has no assistant message", Uncertain: true}
	}
	encoded, _ := json.Marshal(message)
	var result core.Message
	if err := json.Unmarshal(encoded, &result); err != nil || result.Role != "assistant" {
		return core.Completion{}, &core.Error{Code: "upstream_protocol_error", Message: "planner returned an invalid assistant message", Uncertain: true}
	}
	seen := map[string]bool{}
	for _, call := range result.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) || seen[call.ID] {
			return core.Completion{}, &core.Error{Code: "upstream_protocol_error", Message: "planner returned malformed or duplicate function calls", Uncertain: true}
		}
		seen[call.ID] = true
	}
	return core.Completion{Message: result, Usage: usage(m), Model: str(m["model"]), FinishReason: str(choice["finish_reason"])}, nil
}
