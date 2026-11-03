package types

import "encoding/json"

// TraceSchemaVersion2 is the trace schema version that adds conversation
// turns, agent handoffs, retries and recorded request/response pairs.
// Version 1 traces stay valid; every v2 field is optional.
const TraceSchemaVersion2 = 2

// Turn is one conversation turn: a top-level agent invocation and the steps
// it produced. StepIndices index Trace.Steps.
type Turn struct {
	Index       int             `json:"index"`
	SpanID      string          `json:"span_id,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	StepIndices []int           `json:"step_indices"`
}

// Handoff records control passing from one agent to another. StepIndex is
// the agent_call step that starts ToAgent's work.
type Handoff struct {
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	StepIndex int    `json:"step_index"`
}

// StepError marks a step whose span ended in error.
type StepError struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// Exchange is a recorded request/response pair for a model call, kept
// verbatim from the instrumented exchange so replay can serve it later.
type Exchange struct {
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}
