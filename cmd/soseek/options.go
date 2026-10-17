package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/samekind/soseek/internal/core"
)

type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ",") }
func (f *multiFlag) Set(v string) error { *f = append(*f, v); return nil }

type options struct {
	provider, providers, channel, intent, profile, mode, budget, deadline, input, fields string
	limit, maxChars                                                                      int
	fresh, plan, detach, requireAll, full, compact, progress                             bool
	waitTimeout                                                                          time.Duration
	opts                                                                                 multiFlag
	a                                                                                    *app
}

func newOutputOptions(fs *flag.FlagSet, a *app) *options {
	o := &options{a: a}
	fs.BoolVar(&a.json, "json", a.json, "output one final JSON object")
	fs.BoolVar(&a.stream, "stream", a.stream, "output NDJSON progress and result (excludes --json)")
	fs.DurationVar(&o.waitTimeout, "wait-timeout", 5*time.Minute, "local wait timeout; cloud execution continues")
	fs.StringVar(&o.fields, "fields", "", "comma-separated response fields, including results.title")
	fs.IntVar(&o.maxChars, "max-chars", 0, "maximum returned text characters; 0 means no clipping")
	fs.BoolVar(&o.requireAll, "require-all", false, "return exit 4 on a partial result")
	fs.BoolVar(&o.full, "full", false, "return the complete stored result, including raw responses and attempts")
	fs.BoolVar(&o.compact, "compact", false, "return the default compact evidence view (excludes --full)")
	fs.BoolVar(&o.progress, "progress", false, "show run status progress on stderr, including with --json")
	return o
}
func newOptions(fs *flag.FlagSet, a *app) *options {
	o := newOutputOptions(fs, a)
	fs.StringVar(&o.provider, "provider", "", "explicit provider; never switches provider")
	fs.StringVar(&o.providers, "providers", "", "comma-separated provider set for parallel search")
	fs.StringVar(&o.channel, "channel", "", "explicit channel; requires --provider")
	fs.StringVar(&o.intent, "intent", "", "task intent declared by server discovery")
	fs.StringVar(&o.profile, "profile", "", "server profile")
	fs.StringVar(&o.mode, "mode", "", "research mode: agent or pipeline")
	fs.StringVar(&o.budget, "budget-usd", "", "per-run upper budget in USD")
	fs.StringVar(&o.deadline, "deadline", "", "cloud execution deadline, e.g. 180s (independent of local wait)")
	fs.StringVar(&o.input, "input", "", "native-operation JSON object file; - reads stdin")
	fs.IntVar(&o.limit, "limit", 0, "requested number of results (server limits apply)")
	fs.BoolVar(&o.fresh, "fresh", false, "bypass cached upstream results")
	fs.BoolVar(&o.plan, "plan", false, "validate and show plan without upstream calls")
	fs.BoolVar(&o.detach, "detach", false, "return run ID without waiting")
	fs.Var(&o.opts, "opt", "provider.key=value (repeatable); JSON values or plain strings")
	return o
}

// Standard flag parsing stops at the first positional argument. Reordering
// known flags lets documented commands place flags after the query while
// retaining -- as an explicit end-of-options marker.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		name, _, inline := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil || inline {
			continue
		}
		bf, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if isBool && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return fs.Parse(append(append(flags, "--"), positionals...))
}
func (o *options) validateOutput() error {
	if o.a.json && o.a.stream {
		return inputError("--json and --stream are mutually exclusive")
	}
	if o.full && o.compact {
		return inputError("--full and --compact are mutually exclusive")
	}
	if o.maxChars < 0 {
		return inputError("--max-chars cannot be negative")
	}
	if o.waitTimeout <= 0 {
		return inputError("--wait-timeout must be positive")
	}
	if o.fields != "" {
		fields := strings.Split(o.fields, ",")
		for i, field := range fields {
			fields[i] = normalizeField(strings.TrimSpace(field))
			if !validField(fields[i]) {
				return inputError("unsupported --fields value " + strconv.Quote(strings.TrimSpace(field)) + "; use answer, usage, results.title, results.url, or results.content (title/url/content are source-field aliases)")
			}
		}
		o.fields = strings.Join(fields, ",")
	}
	return nil
}
func (o *options) request(operation, value string, in io.Reader) (core.Request, error) {
	if err := o.validateOutput(); err != nil {
		return core.Request{}, err
	}
	if o.plan && (o.detach || o.a.stream) {
		return core.Request{}, inputError("--plan cannot be combined with --detach or --stream")
	}
	if o.detach && o.a.stream {
		return core.Request{}, inputError("--detach and --stream are mutually exclusive")
	}
	if o.provider != "" && o.providers != "" {
		return core.Request{}, inputError("--provider and --providers are mutually exclusive")
	}
	if o.channel != "" && o.provider == "" {
		return core.Request{}, inputError("--channel requires an explicit --provider")
	}
	if o.providers != "" && o.profile != "" {
		return core.Request{}, inputError("--providers and --profile cannot be combined")
	}
	if o.limit < 0 {
		return core.Request{}, inputError("--limit cannot be negative")
	}
	if o.mode != "" && o.mode != "agent" && o.mode != "pipeline" {
		return core.Request{}, inputError("--mode must be agent or pipeline")
	}
	if o.mode != "" && operation != "research" && operation != "answer" {
		return core.Request{}, inputError("--mode applies only to research and answer")
	}
	if (operation == "search" || operation == "read") && o.profile == "deep" {
		return core.Request{}, inputError("deep profile applies only to research and answer")
	}
	if o.budget != "" {
		n, err := core.ParseUSD(o.budget)
		if err != nil || n <= 0 {
			return core.Request{}, inputError("--budget-usd must be a positive USD amount with at most 6 decimal places")
		}
	}
	r := core.Request{Operation: operation, Provider: o.provider, Channel: o.channel, Intent: o.intent, Profile: o.profile, Mode: o.mode, Limit: o.limit, BudgetUSD: o.budget, Fresh: o.fresh}
	if strings.TrimSpace(value) == "" {
		return r, inputError("query or URL cannot be empty")
	}
	if o.providers != "" {
		seen := map[string]bool{}
		for _, p := range strings.Split(o.providers, ",") {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				return r, inputError("--providers contains an empty or repeated provider")
			}
			seen[p] = true
			r.Providers = append(r.Providers, p)
		}
	}
	if o.deadline != "" {
		d, err := time.ParseDuration(o.deadline)
		if seconds, e := strconv.Atoi(o.deadline); e == nil {
			d = time.Duration(seconds) * time.Second
			err = nil
		}
		if err != nil || d <= 0 || d > 24*time.Hour {
			return r, inputError("--deadline must be a positive duration up to 24h, e.g. 180s")
		}
		r.DeadlineSeconds = int(math.Ceil(d.Seconds()))
	}
	switch operation {
	case "read":
		r.URL = value
	case "call":
		provider, op, ok := strings.Cut(value, ".")
		if !ok || provider == "" || op == "" || strings.Contains(op, ".") {
			return r, inputError("native operation must be PROVIDER.OPERATION")
		}
		if o.provider != "" && o.provider != provider {
			return r, inputError("native operation conflicts with --provider")
		}
		if o.providers != "" {
			return r, inputError("native operation cannot use --providers")
		}
		r.Operation = value
		r.Provider = provider
		if o.input == "" {
			return r, inputError("call requires --input FILE (or --input -)")
		}
		var reader io.Reader = in
		var f *os.File
		if o.input != "-" {
			var err error
			f, err = os.Open(o.input)
			if err != nil {
				return r, inputError(err.Error())
			}
			defer f.Close()
			reader = f
		}
		b, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		if err != nil {
			return r, inputError(err.Error())
		}
		if len(b) > 1<<20 {
			return r, inputError("--input exceeds 1 MiB")
		}
		if err = json.Unmarshal(b, &r.Input); err != nil || r.Input == nil {
			return r, inputError("--input must contain one JSON object")
		}
	default:
		r.Query = value
	}
	if operation != "call" && o.input != "" {
		return r, inputError("--input is only supported by call")
	}
	if len(o.opts) > 0 {
		r.Options = map[string]any{}
	}
	for _, opt := range o.opts {
		key, value, ok := strings.Cut(opt, "=")
		provider, field, namespaced := strings.Cut(key, ".")
		if !ok || !namespaced || provider == "" || field == "" {
			return r, inputError("--opt must be provider.key=value")
		}
		if _, exists := r.Options[key]; exists {
			return r, inputError("duplicate --opt key: " + key)
		}
		if r.Provider != "" && provider != r.Provider {
			return r, inputError("--opt namespace does not match --provider")
		}
		if len(r.Providers) > 0 {
			found := false
			for _, p := range r.Providers {
				if p == provider {
					found = true
				}
			}
			if !found {
				return r, inputError("--opt namespace is absent from --providers")
			}
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			decoded = value
		}
		r.Options[key] = decoded
	}
	return r, nil
}

var resultFields = map[string]bool{"schema_version": true, "run_id": true, "status": true, "operation": true, "results": true, "queries": true, "citations": true, "retrieval_events": true, "answer": true, "attempts": true, "usage": true, "routing": true, "warnings": true, "gaps": true, "stop_reason": true, "truncated": true, "raw": true}
var sourceFields = map[string]bool{"source_id": true, "url": true, "canonical_url": true, "title": true, "snippet": true, "content": true, "published_at": true, "fetched_at": true, "found_by": true, "occurrences": true, "score": true, "content_hash": true, "truncated": true}

func validField(field string) bool {
	if resultFields[field] {
		return true
	}
	p, f, ok := strings.Cut(field, ".")
	return ok && p == "results" && sourceFields[f]
}

func normalizeField(field string) string {
	if !resultFields[field] && sourceFields[field] {
		return "results." + field
	}
	return field
}

// resultView only projects stored data. It never starts another upstream call.
func resultView(result core.Result, o *options, server string) (map[string]any, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var original map[string]any
	if err = json.Unmarshal(b, &original); err != nil {
		return nil, err
	}
	view := original
	projected := o.fields != "" || !o.full
	if o.fields != "" {
		view = fieldView(original, o.fields, result)
	} else if !o.full {
		view = compactView(original, result)
	}
	projectionTruncated := false
	if sources, ok := view["results"].([]any); ok {
		for _, item := range sources {
			if source, ok := item.(map[string]any); ok && (source["content_truncated"] == true || source["snippet_truncated"] == true) {
				projectionTruncated = true
			}
		}
	}
	if o.maxChars > 0 {
		remaining := o.maxChars
		if clipText(view, "answer", &remaining) {
			projectionTruncated = true
		}
		if sources, ok := view["results"].([]any); ok {
			for _, item := range sources {
				if source, ok := item.(map[string]any); ok {
					for _, field := range []string{"snippet", "content"} {
						if clipText(source, field, &remaining) {
							projectionTruncated = true
						}
					}
				}
			}
		}
		if _, ok := view["raw"]; ok {
			delete(view, "raw")
			view["raw_available"] = true
			projectionTruncated = true
		}
		// Native citations can duplicate entire upstream answers. Under an
		// explicit text budget keep reference links, never truncated text with
		// offsets that could be mistaken for offsets into the displayed answer.
		if citations, ok := view["citations"].([]any); ok && len(citations) > 0 {
			references := []any{}
			for _, item := range citations {
				if citation, ok := item.(map[string]any); ok {
					ref := map[string]any{}
					copyFields(ref, citation, "source_id", "url")
					references = append(references, ref)
				}
			}
			view["citations"] = references
			view["citation_mode"] = "reference-only; original text and offsets are available with --full"
			view["citations_truncated"] = true
			projectionTruncated = true
		}
	}
	view["truncated"] = result.Truncated || projectionTruncated
	sourceTruncated := result.Truncated
	for _, source := range result.Results {
		sourceTruncated = sourceTruncated || source.Truncated
	}
	view["source_content_truncated"] = sourceTruncated
	view["output_truncated"] = projected || projectionTruncated
	if projected || projectionTruncated {
		view["view"] = "compact"
		if o.fields != "" {
			view["view"] = "fields"
		} else if o.full {
			view["view"] = "full"
		}
		view["full_result_command"] = fmt.Sprintf("soseek runs get %s --full --json", result.RunID)
		for _, source := range result.Results {
			if source.ID != "" {
				view["source_command"] = fmt.Sprintf("soseek runs source %s SOURCE_ID --offset 0 --max-chars 4000 --json", result.RunID)
				break
			}
		}
	}
	return view, nil
}

// Preserve identity, accounting, failures and gaps even under an explicit field
// projection. Selected fields always come from the full original response.
func fieldView(original map[string]any, fields string, result core.Result) map[string]any {
	selected := map[string]any{}
	sourceSelection := map[string]bool{}
	for _, field := range strings.Split(fields, ",") {
		field = normalizeField(strings.TrimSpace(field))
		if strings.HasPrefix(field, "results.") {
			sourceSelection[strings.TrimPrefix(field, "results.")] = true
		} else if value, ok := original[field]; ok {
			selected[field] = value
		}
	}
	if len(sourceSelection) > 0 && selected["results"] == nil {
		filtered := []any{}
		if sources, ok := original["results"].([]any); ok {
			for _, item := range sources {
				source, ok := item.(map[string]any)
				if !ok {
					continue
				}
				entry := map[string]any{}
				for _, field := range []string{"source_id", "url", "truncated"} {
					if value, ok := source[field]; ok {
						entry[field] = value
					}
				}
				for field := range sourceSelection {
					if value, ok := source[field]; ok {
						entry[field] = value
					}
				}
				filtered = append(filtered, entry)
			}
		}
		selected["results"] = filtered
	}
	copyFields(selected, original, "schema_version", "run_id", "status", "operation", "usage", "warnings", "gaps", "stop_reason", "queries")
	if issues := attemptIssues(result.Attempts); len(issues) > 0 {
		selected["attempt_issues"] = issues
	}
	// Keeping an answer without its source identities would break [S1] references.
	if answer, _ := selected["answer"].(string); answer != "" && selected["results"] == nil {
		refs := []any{}
		for _, source := range result.Results {
			refs = append(refs, map[string]any{"source_id": source.ID, "url": source.URL})
		}
		selected["results"] = refs
	}
	return selected
}

func compactView(original map[string]any, result core.Result) map[string]any {
	view := map[string]any{}
	copyFields(view, original, "schema_version", "run_id", "status", "operation", "answer", "usage", "warnings", "gaps", "stop_reason", "queries")
	sources := []any{}
	for _, source := range result.Results {
		entry := map[string]any{"url": source.URL}
		if source.ID != "" {
			entry["source_id"] = source.ID
		}
		if source.Title != "" {
			entry["title"] = source.Title
		}
		if source.PublishedAt != "" {
			entry["published_at"] = source.PublishedAt
		}
		if len(source.FoundBy) > 0 {
			entry["found_by"] = source.FoundBy
		}
		queryIDs := []string{}
		for _, occurrence := range source.Occurrences {
			if occurrence.QueryID != "" && !containsString(queryIDs, occurrence.QueryID) {
				queryIDs = append(queryIDs, occurrence.QueryID)
			}
		}
		if len(queryIDs) > 0 {
			entry["query_ids"] = queryIDs
		}
		if source.Truncated {
			entry["source_content_truncated"] = true
		}
		entry["evidence_type"] = "reference"
		if source.Snippet != "" {
			entry["snippet"] = source.Snippet
			entry["evidence_type"] = "snippet"
			remaining := 500
			clipText(entry, "snippet", &remaining)
		}
		if source.Content != "" {
			limit := 0
			switch result.Operation {
			case "read":
				limit = 12000
			case "research", "answer":
				limit = 1200
			default:
				if strings.HasSuffix(result.Operation, ".contents") || strings.HasSuffix(result.Operation, ".extract") || strings.HasSuffix(result.Operation, ".scrape") {
					limit = 12000
				} else if result.Operation != "search" {
					limit = 1200
				}
			}
			if limit > 0 {
				entry["evidence_type"] = "content"
			} else if source.Snippet == "" {
				entry["snippet"] = source.Content
				entry["snippet_from"] = "content"
				entry["evidence_type"] = "content_excerpt"
				remaining := 500
				clipText(entry, "snippet", &remaining)
			}
			entry["content"] = source.Content
			remaining := limit
			clipText(entry, "content", &remaining)
			if limit == 0 {
				delete(entry, "content")
			}
		}
		sources = append(sources, entry)
	}
	view["results"] = sources
	if issues := attemptIssues(result.Attempts); len(issues) > 0 {
		view["attempt_issues"] = issues
	}
	references := []any{}
	seen := map[string]bool{}
	for _, citation := range result.Citations {
		// Upstream offsets refer to upstream answer_text, not necessarily Result.Answer.
		// This view only gives links that are actually mentioned in the displayed answer.
		cited := citation.SourceID != "" && strings.Contains(result.Answer, "["+citation.SourceID+"]") || citation.URL != "" && strings.Contains(result.Answer, citation.URL)
		key := citation.SourceID + "\n" + citation.URL
		if !cited || seen[key] {
			continue
		}
		seen[key] = true
		ref := map[string]any{"url": citation.URL}
		if citation.SourceID != "" {
			ref["source_id"] = citation.SourceID
		}
		references = append(references, ref)
	}
	if len(references) > 0 {
		view["citations"] = references
		view["citation_mode"] = "reference-only; original text and offsets are available with --full"
	}
	if len(result.Raw) > 0 {
		view["raw_available"] = true
	}
	return view
}
func copyFields(dst, src map[string]any, fields ...string) {
	for _, field := range fields {
		if value, ok := src[field]; ok {
			dst[field] = value
		}
	}
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A compact output must retain costly uncertainty and failures even when a run
// delivered useful evidence. Successful known-cost attempts stay in --full.
func attemptIssues(attempts []core.Attempt) []any {
	issues := []any{}
	for _, attempt := range attempts {
		unknown := !attempt.Cached && !attempt.Usage.CostKnown && attempt.Usage.ReportedMicros == 0 && attempt.Usage.EstimatedMicros == 0
		if attempt.Error == nil && attempt.Status == "succeeded" && !unknown && attempt.Usage.UnsettledAttempts == 0 && attempt.Usage.ReservedMicros == 0 {
			continue
		}
		issue := map[string]any{"provider": attempt.Provider, "channel": attempt.Channel, "operation": attempt.Operation, "status": attempt.Status, "usage": attempt.Usage}
		if attempt.ID != "" {
			issue["id"] = attempt.ID
		}
		if attempt.MaxMicros > 0 {
			issue["max_micros"] = attempt.MaxMicros
		}
		if attempt.Error != nil {
			issue["error"] = attempt.Error
		}
		if unknown {
			issue["cost_unknown"] = true
		}
		issues = append(issues, issue)
	}
	return issues
}

// Lengths and offsets are Unicode code points, matching runs source windows.
func clipText(target map[string]any, field string, remaining *int) bool {
	text, ok := target[field].(string)
	if !ok {
		return false
	}
	runes := []rune(text)
	if len(runes) <= *remaining {
		*remaining -= len(runes)
		return false
	}
	total := len(runes)
	if original, ok := target[field+"_total_chars"].(int); ok && original > total {
		total = original
	}
	target[field] = string(runes[:*remaining])
	target[field+"_truncated"] = true
	target[field+"_total_chars"] = total
	if field == "content" {
		target["content_available"] = true
	}
	*remaining = 0
	return true
}
