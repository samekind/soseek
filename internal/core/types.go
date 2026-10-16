// Package core defines the versioned protocol shared by the service and CLI.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const Version = "0.1.0"
const SchemaVersion = "1.0"

type Config struct {
	Listen         string    `json:"listen"`
	Database       string    `json:"database"`
	ArtifactDir    string    `json:"artifact_dir"`
	Channels       []Channel `json:"channels"`
	SearchOrder    []string  `json:"search_order"`
	ReadOrder      []string  `json:"read_order"`
	PlannerChannel string    `json:"planner_channel"`
	Limits         Limits    `json:"limits"`
	Revision       string    `json:"-"`
}
type Limits struct {
	Workers          int   `json:"workers"`
	Queue            int   `json:"queue"`
	DailyMicros      int64 `json:"daily_micros"`
	DefaultRunMicros int64 `json:"default_run_micros"`
	MaxRunMicros     int64 `json:"max_run_micros"`
	MaxCalls         int   `json:"max_calls"`
	MaxRounds        int   `json:"max_rounds"`
	MaxPages         int   `json:"max_pages"`
	MaxChars         int   `json:"max_chars"`
	MaxDeadline      int   `json:"max_deadline_seconds"`
	RetentionDays    int   `json:"retention_days"`
	CacheSeconds     int   `json:"cache_seconds"`
}
type Channel struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	BaseURL        string   `json:"base_url"`
	KeyFile        string   `json:"key_file,omitempty"`
	KeyEnv         string   `json:"key_env,omitempty"`
	Model          string   `json:"model,omitempty"`
	QuotaGroup     string   `json:"quota_group"`
	RPM            int      `json:"rpm"`
	Concurrency    int      `json:"concurrency"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Operations     []string `json:"operations"`
	// CostBounds are administrator-declared per-operation ceilings, in micro-USD.
	CostBounds map[string]int64  `json:"cost_bounds_micros"`
	Verified   map[string]string `json:"verified,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
}
type Principal struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	DailyMicros      int64    `json:"daily_micros"`
	AllowedProviders []string `json:"allowed_providers,omitempty"`
}

func (p Principal) Allows(provider string) bool {
	if len(p.AllowedProviders) == 0 {
		return true
	}
	for _, v := range p.AllowedProviders {
		if v == provider {
			return true
		}
	}
	return false
}

type Request struct {
	Operation       string         `json:"operation"`
	Query           string         `json:"query,omitempty"`
	URL             string         `json:"url,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Providers       []string       `json:"providers,omitempty"`
	Channel         string         `json:"channel,omitempty"`
	Intent          string         `json:"intent,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	Limit           int            `json:"limit,omitempty"`
	BudgetUSD       string         `json:"budget_usd,omitempty"`
	DeadlineSeconds int            `json:"deadline_seconds,omitempty"`
	Options         map[string]any `json:"options,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Fresh           bool           `json:"fresh,omitempty"`
}
type Plan struct {
	SchemaVersion string     `json:"schema_version"`
	Request       Request    `json:"request"`
	Revision      string     `json:"revision"`
	BudgetUSD     string     `json:"budget_usd"`
	MaxCostUSD    string     `json:"max_cost_usd"`
	Steps         []PlanStep `json:"steps"`
	Warnings      []string   `json:"warnings,omitempty"`
	Dynamic       bool       `json:"dynamic"`
}
type PlanStep struct {
	Provider   string `json:"provider"`
	Channel    string `json:"channel"`
	Operation  string `json:"operation"`
	Reason     string `json:"reason"`
	MaxCostUSD string `json:"max_cost_usd"`
}
type Run struct {
	ID              string    `json:"run_id"`
	PrincipalID     string    `json:"-"`
	Principal       Principal `json:"-"`
	Status          string    `json:"status"`
	Request         Request   `json:"request"`
	Plan            Plan      `json:"plan"`
	Revision        string    `json:"revision"`
	BudgetMicros    int64     `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Deadline        time.Time `json:"deadline"`
	CancelRequested bool      `json:"cancel_requested"`
	Result          *Result   `json:"result,omitempty"`
	Error           *Error    `json:"error,omitempty"`
}

func Terminal(status string) bool {
	switch status {
	case "succeeded", "partial", "failed", "cancelled":
		return true
	}
	return false
}

type Result struct {
	SchemaVersion   string           `json:"schema_version"`
	RunID           string           `json:"run_id"`
	Status          string           `json:"status"`
	Operation       string           `json:"operation"`
	Results         []Source         `json:"results"`
	Queries         []EvidenceQuery  `json:"queries,omitempty"`
	Citations       []Citation       `json:"citations,omitempty"`
	RetrievalEvents []RetrievalEvent `json:"retrieval_events,omitempty"`
	Answer          string           `json:"answer,omitempty"`
	Attempts        []Attempt        `json:"attempts"`
	Usage           Usage            `json:"usage"`
	Routing         []PlanStep       `json:"routing,omitempty"`
	Warnings        []string         `json:"warnings,omitempty"`
	Gaps            []string         `json:"gaps,omitempty"`
	StopReason      string           `json:"stop_reason,omitempty"`
	Truncated       bool             `json:"truncated"`
	Raw             json.RawMessage  `json:"raw,omitempty"`
}

type EvidenceQuery struct {
	ID        string   `json:"id"`
	Query     string   `json:"query"`
	Providers []string `json:"providers,omitempty"`
}

// Citation offsets refer to AnswerText as supplied by upstream. They are not
// silently reinterpreted as offsets into a later synthesized answer.
type Citation struct {
	SourceID   string `json:"source_id,omitempty"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	AnswerText string `json:"answer_text,omitempty"`
	StartIndex *int   `json:"start_index,omitempty"`
	EndIndex   *int   `json:"end_index,omitempty"`
	OffsetUnit string `json:"offset_unit,omitempty"`
}
type RetrievalEvent struct {
	ID     string   `json:"id,omitempty"`
	Type   string   `json:"type"`
	Status string   `json:"status,omitempty"`
	Query  string   `json:"query,omitempty"`
	URLs   []string `json:"urls,omitempty"`
}
type Source struct {
	ID           string       `json:"source_id,omitempty"`
	URL          string       `json:"url"`
	CanonicalURL string       `json:"canonical_url,omitempty"`
	Title        string       `json:"title,omitempty"`
	Snippet      string       `json:"snippet,omitempty"`
	Content      string       `json:"content,omitempty"`
	PublishedAt  string       `json:"published_at,omitempty"`
	FetchedAt    time.Time    `json:"fetched_at,omitempty"`
	FoundBy      []string     `json:"found_by,omitempty"`
	Occurrences  []Occurrence `json:"occurrences,omitempty"`
	Score        float64      `json:"score,omitempty"`
	ContentHash  string       `json:"content_hash,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
}
type Occurrence struct {
	Provider string `json:"provider"`
	Channel  string `json:"channel"`
	QueryID  string `json:"query_id,omitempty"`
	Rank     int    `json:"rank,omitempty"`
}
type Usage struct {
	CostKnown         bool    `json:"cost_known"`
	ReportedMicros    int64   `json:"reported_micros"`
	EstimatedMicros   int64   `json:"estimated_micros"`
	ReservedMicros    int64   `json:"reserved_micros"`
	UnsettledAttempts int     `json:"unsettled_attempts"`
	InputTokens       int64   `json:"input_tokens,omitempty"`
	OutputTokens      int64   `json:"output_tokens,omitempty"`
	NativeCalls       int64   `json:"native_calls,omitempty"`
	Credits           float64 `json:"credits,omitempty"`
}
type Attempt struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Provider   string     `json:"provider"`
	Channel    string     `json:"channel"`
	Operation  string     `json:"operation"`
	Status     string     `json:"status"`
	MaxMicros  int64      `json:"max_micros"`
	Usage      Usage      `json:"usage"`
	Error      *Error     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Cached     bool       `json:"cached,omitempty"`
}
type Event struct {
	ID        int64           `json:"id"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
type Error struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable,omitempty"`
	Uncertain         bool   `json:"uncertain,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func (e *Error) Error() string      { return e.Code + ": " + e.Message }
func E(code, message string) *Error { return &Error{Code: code, Message: message} }

type Call struct {
	Operation string
	Query     string
	URL       string
	Limit     int
	Options   map[string]any
	Input     map[string]any
	// Progress persists upstream job identifiers before a long operation is polled.
	Progress func(kind string, data any) error `json:"-"`
}
type Output struct {
	Sources         []Source
	Answer          string
	Usage           Usage
	Raw             json.RawMessage
	Warnings        []string
	Citations       []Citation
	RetrievalEvents []RetrievalEvent
}
type Operation struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type Tool struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
type Completion struct {
	Message      Message
	Usage        Usage
	Model        string
	FinishReason string
}
type ProviderClient interface {
	Call(context.Context, Channel, Call) (Output, error)
	Complete(context.Context, Channel, []Message, []Tool, int) (Completion, error)
	Operations(provider string) []Operation
}

// ExecutionStore persists accounting before a request is sent. An unfinished
// attempt must stay reserved and must not be replayed after a process crash.
type ExecutionStore interface {
	BeginAttempt(ctx context.Context, runID string, a Attempt) (Attempt, error)
	EndAttempt(ctx context.Context, a Attempt) error
	Attempts(ctx context.Context, runID string) ([]Attempt, error)
	AddEvent(ctx context.Context, runID, kind string, data any) error
	CacheGet(ctx context.Context, principalID, key string) (*Output, error)
	CachePut(ctx context.Context, principalID, key string, out Output, ttl time.Duration) error
	Checkpoint(ctx context.Context, runID string, data any) error
	Cancelled(ctx context.Context, runID string) bool
}

func ParseUSD(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	p := strings.Split(s, ".")
	if len(p) > 2 || p[0] == "" {
		return 0, fmt.Errorf("invalid USD amount")
	}
	for _, part := range p {
		if part == "" {
			return 0, fmt.Errorf("invalid USD amount")
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("invalid USD amount")
			}
		}
	}
	whole, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil || whole > 1000000 {
		return 0, fmt.Errorf("invalid USD amount")
	}
	frac := ""
	if len(p) == 2 {
		frac = p[1]
	}
	if len(frac) > 6 {
		return 0, fmt.Errorf("maximum 6 decimal places")
	}
	frac += strings.Repeat("0", 6-len(frac))
	f, e := strconv.ParseInt(frac, 10, 64)
	if e != nil {
		return 0, fmt.Errorf("invalid USD amount")
	}
	return whole*1000000 + f, nil
}
func USD(m int64) string { return fmt.Sprintf("%d.%06d", m/1000000, m%1000000) }
