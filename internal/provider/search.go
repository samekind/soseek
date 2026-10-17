package provider

import (
	"strings"

	"github.com/samekind/soseek/internal/core"
)

const searchHighlightCharacters = 500

// StandardSearchCall adds a small, query-relevant excerpt to ordinary Exa
// searches. Call this only for soseek's standard search entry point: native
// operation payloads keep their original meaning, including absent contents.
//
// The documented /search protocol bundles highlights with search; /contents and
// AI summaries have separate pricing. Compatible channels can price differently,
// so this still requires the channel's normal administrator-declared search cost
// ceiling and the executor's reservation. No price or "free" assumption is used.
// https://exa.ai/docs/search/highlights
// https://exa.ai/docs/admin/pricing
func StandardSearchCall(channel core.Channel, call core.Call) core.Call {
	if channel.Provider != "exa" || call.Operation != "search" || strings.TrimSpace(call.Query) == "" {
		return call
	}
	for _, values := range []map[string]any{call.Input, call.Options} {
		for name := range values {
			if strings.TrimPrefix(name, "exa.") == "contents" {
				return call
			}
		}
	}
	options := make(map[string]any, len(call.Options)+1)
	for name, value := range call.Options {
		options[name] = value
	}
	options["contents"] = map[string]any{"highlights": map[string]any{"query": call.Query, "maxCharacters": searchHighlightCharacters}}
	call.Options = options
	return call
}
