package types

import "encoding/json"

const (
	StatusPass     = "pass"
	StatusSoftFail = "soft_fail"
	StatusHardFail = "hard_fail"

	TypeSchema     = "schema"
	TypeConstraint = "constraint"
	TypeTrace      = "trace"
	TypeContent    = "content"
	TypeEmbedding  = "embedding"
	TypeLLMJudge   = "llm_judge"
	TypeTraceTree  = "trace_tree"
)

// Assertion defines an assertion to evaluate against a trace.
type Assertion struct {
	AssertionID string          `json:"assertion_id"`
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
	RequestID   string          `json:"request_id,omitempty"`
}

// AssertionResult holds the result of evaluating a single assertion.
type AssertionResult struct {
	AssertionID string  `json:"assertion_id"`
	Status      string  `json:"status"`
	Score       float64 `json:"score"`
	Explanation string  `json:"explanation"`
	Cost        float64 `json:"cost"`
	DurationMS  int64   `json:"duration_ms"`
	RequestID   string  `json:"request_id,omitempty"`
}
