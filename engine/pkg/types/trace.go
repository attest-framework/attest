package types

import "encoding/json"

const (
	StepTypeLLMCall   = "llm_call"
	StepTypeToolCall  = "tool_call"
	StepTypeRetrieval = "retrieval"
	StepTypeAgentCall = "agent_call"
)

// Trace represents a single agent execution trace.
type Trace struct {
	SchemaVersion int              `json:"schema_version"`
	TraceID       string           `json:"trace_id"`
	AgentID       string           `json:"agent_id"`
	Input         json.RawMessage  `json:"input"`
	Steps         []Step           `json:"steps"`
	Output        json.RawMessage  `json:"output"`
	Metadata      *TraceMetadata   `json:"metadata,omitempty"`
	ParentTraceID *string          `json:"parent_trace_id,omitempty"`
}

// Step represents a single step within a trace.
type Step struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	Result   json.RawMessage `json:"result"`
	SubTrace *Trace          `json:"sub_trace,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// TraceMetadata holds optional metadata about a trace execution.
type TraceMetadata struct {
	TotalTokens *int     `json:"total_tokens,omitempty"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	LatencyMS   *int     `json:"latency_ms,omitempty"`
	Model       *string  `json:"model,omitempty"`
	Timestamp   *string  `json:"timestamp,omitempty"`

	// Aggregate fields for multi-agent trace trees.
	AggregateTokens    *int     `json:"aggregate_tokens,omitempty"`
	AggregateCostUSD   *float64 `json:"aggregate_cost_usd,omitempty"`
	AggregateLatencyMS *int     `json:"aggregate_latency_ms,omitempty"`
	AgentCount         *int     `json:"agent_count,omitempty"`
}
