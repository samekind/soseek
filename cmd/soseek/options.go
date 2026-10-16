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

	"github.com/jstar0/soseek/internal/core"
)

type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ",") }
func (f *multiFlag) Set(v string) error { *f = append(*f, v); return nil }

type options struct {
	provider, providers, channel, intent, profile, mode, budget, deadline, input, fields string
	limit, maxChars                                                                      int
	fresh, plan, detach, requireAll                                                      bool
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
	if o.maxChars < 0 {
		return inputError("--max-chars cannot be negative")
	}
	if o.waitTimeout <= 0 {
		return inputError("--wait-timeout must be positive")
	}
	if o.fields != "" {
		for _, field := range strings.Split(o.fields, ",") {
			if !validField(strings.TrimSpace(field)) {
				return inputError("unsupported --fields value; use result fields or results.FIELD")
			}
		}
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

var resultFields = map[string]bool{"schema_version": true, "run_id": true, "status": true, "operation": true, "results": true, "answer": true, "attempts": true, "usage": true, "routing": true, "warnings": true, "gaps": true, "stop_reason": true, "truncated": true, "raw": true}
var sourceFields = map[string]bool{"source_id": true, "url": true, "canonical_url": true, "title": true, "snippet": true, "content": true, "published_at": true, "fetched_at": true, "found_by": true, "occurrences": true, "score": true, "content_hash": true, "truncated": true}

func validField(field string) bool {
	if resultFields[field] {
		return true
	}
	p, f, ok := strings.Cut(field, ".")
	return ok && p == "results" && sourceFields[f]
}

func resultView(result core.Result, o *options, server string) (map[string]any, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err = json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	truncated := result.Truncated
	if o.fields != "" {
		selected := map[string]any{}
		sourceSelection := map[string]bool{}
		for _, field := range strings.Split(o.fields, ",") {
			field = strings.TrimSpace(field)
			if strings.HasPrefix(field, "results.") {
				sourceSelection[strings.TrimPrefix(field, "results.")] = true
			} else if v, ok := obj[field]; ok {
				selected[field] = v
			}
		}
		if len(sourceSelection) > 0 {
			items, _ := obj["results"].([]any)
			filtered := make([]any, 0, len(items))
			for _, item := range items {
				src, ok := item.(map[string]any)
				if !ok {
					continue
				}
				dst := map[string]any{}
				for _, key := range []string{"source_id", "url", "truncated"} {
					if v, exists := src[key]; exists {
						dst[key] = v
					}
				}
				for key := range sourceSelection {
					if v, exists := src[key]; exists {
						dst[key] = v
					}
				}
				filtered = append(filtered, dst)
			}
			selected["results"] = filtered
		}
		for _, key := range []string{"schema_version", "run_id", "status", "operation", "warnings", "gaps", "stop_reason"} {
			if v, exists := obj[key]; exists {
				selected[key] = v
			}
		}
		obj = selected
		truncated = true
	}
	if o.maxChars > 0 {
		remaining := o.maxChars
		clip := func(m map[string]any, key string) bool {
			s, ok := m[key].(string)
			if !ok {
				return false
			}
			r := []rune(s)
			if len(r) <= remaining {
				remaining -= len(r)
				return false
			}
			m[key] = string(r[:remaining])
			remaining = 0
			truncated = true
			return true
		}
		clip(obj, "answer")
		if items, ok := obj["results"].([]any); ok {
			for _, item := range items {
				if src, ok := item.(map[string]any); ok {
					cut := false
					for _, key := range []string{"title", "snippet", "content"} {
						cut = clip(src, key) || cut
					}
					if cut {
						src["truncated"] = true
					}
				}
			}
		}
		// Raw responses cannot be safely clipped without changing their shape.
		if _, ok := obj["raw"]; ok {
			delete(obj, "raw")
			truncated = true
		}
	}
	obj["truncated"] = truncated
	if truncated {
		obj["full_result_url"] = server + "/v1/runs/" + result.RunID + "/result"
		obj["full_result_command"] = fmt.Sprintf("soseek runs get %s --json", result.RunID)
	}
	return obj, nil
}
