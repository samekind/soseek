// Package provider implements the upstream protocols. It never retries a request:
// a lost response may already have incurred a charge.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/samekind/soseek/internal/core"
)

const maxResponseBytes = 8 << 20

type Client struct{ http *http.Client }

func New() *Client { return NewWithHTTP(&http.Client{}) }

// NewWithHTTP preserves the caller's transport but never forwards authentication
// across redirects (including same-host redirects to another port).
func NewWithHTTP(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{}
	}
	copy := *h
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copy}
}

var _ core.ProviderClient = (*Client)(nil)

// Validate runs precisely the same input checks as Call, without reading a key
// or making a network request. It is safe to use while building a plan.
func (c *Client) Validate(ch core.Channel, call core.Call) error {
	op := operationName(ch.Provider, call.Operation)
	spec, ok := operationSchemas[ch.Provider][op]
	if !ok {
		return core.E("unsupported_operation", "operation is not implemented for this provider")
	}
	_, err := prepare(ch.Provider, op, call, spec)
	return err
}

func operationName(provider, operation string) string {
	op := strings.TrimPrefix(operation, provider+".")
	if op == "read" {
		switch provider {
		case "exa":
			return "contents"
		case "tavily":
			return "extract"
		case "firecrawl":
			return "scrape"
		}
	}
	return op
}

func (c *Client) Call(ctx context.Context, ch core.Channel, call core.Call) (core.Output, error) {
	op := operationName(ch.Provider, call.Operation)
	schema, ok := operationSchemas[ch.Provider][op]
	if !ok {
		return core.Output{}, core.E("unsupported_operation", "operation is not implemented for this provider")
	}
	body, err := prepare(ch.Provider, op, call, schema)
	if err != nil {
		return core.Output{}, err
	}
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	key, err := readKey(ch)
	if err != nil {
		return core.Output{}, err
	}
	var out core.Output
	switch ch.Provider {
	case "exa":
		out, err = c.exa(ctx, ch, key, op, body)
	case "tavily":
		out, err = c.tavily(ctx, ch, key, op, body)
	case "firecrawl":
		out, err = c.firecrawl(ctx, ch, key, op, body, call.Progress)
	case "serper":
		out, err = c.serper(ctx, ch, key, op, body)
	case "grok":
		out, err = c.grokSearch(ctx, ch, key, body)
	}
	if err != nil {
		return core.Output{}, err
	}
	for i := range out.Sources {
		out.Sources[i].FoundBy = []string{ch.Provider}
		out.Sources[i].FetchedAt = time.Now().UTC()
		rank := i + 1
		if len(out.Sources[i].Occurrences) > 0 {
			rank = out.Sources[i].Occurrences[0].Rank
		}
		if ch.Provider == "grok" || op == "answer" || op == "contents" || op == "extract" || op == "scrape" || op == "map" || op == "crawl" {
			rank = 0
		}
		out.Sources[i].Occurrences = []core.Occurrence{{Provider: ch.Provider, Channel: ch.ID, Rank: rank}}
	}
	if out.Usage.ReportedMicros == 0 && out.Usage.EstimatedMicros == 0 && out.Usage.UnsettledAttempts > 0 {
		out.Warnings = append(out.Warnings, "upstream did not report a monetary cost; charge remains unknown")
	}
	return out, nil
}

func readKey(ch core.Channel) (string, error) {
	if (ch.KeyFile == "") == (ch.KeyEnv == "") {
		return "", core.E("credential_error", "configure exactly one credential file or environment reference")
	}
	var key string
	if ch.KeyEnv != "" {
		key = os.Getenv(ch.KeyEnv)
	} else {
		f, err := os.Open(ch.KeyFile)
		if err != nil {
			return "", core.E("credential_error", "credential file could not be read")
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 64<<10))
		if err != nil || len(b) >= 64<<10 {
			return "", core.E("credential_error", "credential file is invalid")
		}
		key = string(b)
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return "", core.E("credential_error", "credential is empty or invalid")
	}
	return key, nil
}

func endpoint(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", core.E("configuration_error", "upstream base URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	rel, err := url.Parse(path)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.Fragment != "" {
		return "", core.E("configuration_error", "invalid upstream operation path")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(rel.Path, "/")
	u.RawQuery = rel.RawQuery
	u.RawPath = ""
	return u.String(), nil
}

func (c *Client) request(ctx context.Context, ch core.Channel, key, method, path string, body any) (map[string]any, json.RawMessage, error) {
	addr, err := endpoint(ch.BaseURL, path)
	if err != nil {
		return nil, nil, err
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, nil, core.E("invalid_input", "request cannot be encoded as JSON")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, addr, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, core.E("configuration_error", "upstream request could not be constructed")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch ch.Provider {
	case "exa":
		req.Header.Set("x-api-key", key)
	case "serper":
		req.Header.Set("X-API-KEY", key)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	var sent atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { sent.Store(true) }}))
	resp, err := c.http.Do(req)
	if err != nil {
		code, msg := "upstream_unavailable", "upstream connection failed"
		if ctx.Err() != nil {
			code, msg = "upstream_timeout", "upstream request deadline or cancellation reached"
		}
		if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
			code, msg = "upstream_timeout", "upstream request timed out"
		}
		return nil, nil, &core.Error{Code: code, Message: msg, Retryable: !sent.Load(), Uncertain: sent.Load()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, statusError(resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, &core.Error{Code: "upstream_read_error", Message: "upstream response was interrupted", Uncertain: true}
	}
	if len(data) > maxResponseBytes {
		return nil, nil, &core.Error{Code: "upstream_response_too_large", Message: "upstream response exceeds 8 MiB", Uncertain: true}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, nil, &core.Error{Code: "upstream_protocol_error", Message: "upstream returned invalid JSON", Uncertain: true}
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, nil, &core.Error{Code: "upstream_protocol_error", Message: "upstream returned trailing data", Uncertain: true}
	}
	// Do not trust an upstream echo to keep credentials out of persisted artifacts.
	redact(obj, key)
	if obj["error"] != nil {
		return nil, nil, &core.Error{Code: "upstream_operation_failed", Message: "upstream reported an error in a successful HTTP response", Uncertain: true}
	}
	data, _ = json.Marshal(obj)
	return obj, data, nil
}

func statusError(status int, retry string) *core.Error {
	e := &core.Error{Code: "upstream_error", Message: "upstream returned HTTP " + strconv.Itoa(status)}
	switch {
	case status == 401:
		e.Code = "upstream_authentication"
	case status == 402:
		e.Code = "upstream_quota"
	case status == 403:
		e.Code = "upstream_forbidden"
	case status == 429:
		e.Code = "upstream_rate_limited"
		e.Retryable = true
		e.RetryAfterSeconds = retryAfter(retry)
	case status == 408 || status == 504:
		e.Code = "upstream_timeout"
		e.Uncertain = true
	case status >= 300 && status < 400:
		e.Code = "upstream_redirect"
		e.Message = "upstream redirects are disabled to protect credentials"
	case status >= 500:
		e.Uncertain = true
	case status >= 400:
		e.Code = "upstream_invalid_request"
	}
	return e
}

func retryAfter(s string) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	if t, err := http.ParseTime(s); err == nil {
		n := int(math.Ceil(time.Until(t).Seconds()))
		if n > 0 {
			return n
		}
	}
	return 0
}

func redact(v any, key string) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			cleanKey := strings.ReplaceAll(k, key, "[REDACTED]")
			if cleanKey != k {
				delete(x, k)
				x[cleanKey] = v
				k = cleanKey
			}
			if s, ok := v.(string); ok {
				x[k] = strings.ReplaceAll(s, key, "[REDACTED]")
			} else {
				redact(v, key)
			}
		}
	case []any:
		for i, v := range x {
			if s, ok := v.(string); ok {
				x[i] = strings.ReplaceAll(s, key, "[REDACTED]")
			} else {
				redact(v, key)
			}
		}
	}
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func str(v any) string            { s, _ := v.(string); return s }
func num(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func firstString(m map[string]any, names ...string) string {
	for _, n := range names {
		if s := str(m[n]); s != "" {
			return s
		}
	}
	return ""
}
func firstNumber(m map[string]any, names ...string) float64 {
	for _, n := range names {
		if m[n] != nil {
			return num(m[n])
		}
	}
	return 0
}

func usage(m map[string]any) core.Usage {
	u := object(m["usage"])
	r := core.Usage{UnsettledAttempts: 1, InputTokens: int64(firstNumber(u, "input_tokens", "prompt_tokens")), OutputTokens: int64(firstNumber(u, "output_tokens", "completion_tokens")), Credits: firstNumber(m, "creditsUsed", "credits_used", "credits")}
	// The total already includes the detailed tool counters. Never add the
	// total and server_side_tool_usage_details together.
	if total, ok := u["num_server_side_tools_used"].(json.Number); ok {
		if count, err := total.Int64(); err == nil && count >= 0 {
			r.NativeCalls = count
		}
	}
	if r.Credits == 0 {
		r.Credits = firstNumber(u, "credits", "credits_used")
	}
	var dollars any
	if cost := object(m["costDollars"]); cost != nil {
		dollars = cost["total"]
	}
	if dollars == nil {
		dollars = u["cost_usd"]
	}
	if dollars == nil {
		dollars = m["cost_usd"]
	}
	if value, ok := costMicros(dollars, 1000000, 1); ok {
		r.ReportedMicros = value
		r.UnsettledAttempts = 0
		r.CostKnown = true
	}
	ticks := u["cost_in_usd_ticks"]
	if ticks == nil {
		ticks = m["cost_in_usd_ticks"]
	}
	if value, ok := costMicros(ticks, 1, 10000); ok {
		r.ReportedMicros = value
		r.UnsettledAttempts = 0
		r.CostKnown = true
	}
	return r
}

// Monetary values stay decimal/rational until converted to integer micro-USD.
// Round up sub-micro charges so a measured cost is never undercounted.
func costMicros(v any, multiplier, divisor int64) (int64, bool) {
	value, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(string(value))
	if !ok || r.Sign() < 0 {
		return 0, false
	}
	r.Mul(r, big.NewRat(multiplier, divisor))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}
