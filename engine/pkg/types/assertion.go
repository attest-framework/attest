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
	TypePlugin     = "plugin"

	// ThresholdSource* values describe how the engine classified an
	// assertion's status. They surface dynamic-threshold degradation so
	// callers can distinguish "evaluated against baseline" from "fell back
	// because the baseline could not be loaded".
	ThresholdSourceStatic             = "static"
	ThresholdSourceDynamic            = "dynamic"
	ThresholdSourceDynamicUnavailable = "dynamic_unavailable"
)

// LayerForType returns the assertion-pipeline layer (1–8) for a given
// assertion type string, or 0 if the type is unknown. Layers map to:
// 1 schema, 2 constraint, 3 trace and trace_tree, 4 content,
// 5 embedding, 6 llm_judge, 7 (reserved for trace_tree higher-order checks
// once they migrate), 8 plugin.
func LayerForType(assertionType string) int {
	switch assertionType {
	case TypeSchema:
		return 1
	case TypeConstraint:
		return 2
	case TypeTrace, TypeTraceTree:
		return 3
	case TypeContent:
		return 4
	case TypeEmbedding:
		return 5
	case TypeLLMJudge:
		return 6
	case TypePlugin:
		return 8
	default:
		return 0
	}
}

// Assertion defines an assertion to evaluate against a trace.
type Assertion struct {
	AssertionID string          `json:"assertion_id"`
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
	RequestID   string          `json:"request_id,omitempty"`
}

// JudgeMetadata captures the audit trail for an LLM-judge evaluation so
// reports can show *which* judge produced the score, against which rubric
// version, and how stable the score is across repeated samples. Only
// populated for llm_judge results.
type JudgeMetadata struct {
	Model            string    `json:"model,omitempty"`
	RubricName       string    `json:"rubric_name,omitempty"`
	RubricVersion    string    `json:"rubric_version,omitempty"`
	PromptHash       string    `json:"prompt_hash,omitempty"`
	SampleScores     []float64 `json:"sample_scores,omitempty"`
	ScoreMean        float64   `json:"score_mean,omitempty"`
	ScoreStddev      float64   `json:"score_stddev,omitempty"`
	HighVarianceFlag bool      `json:"high_variance,omitempty"`
}

// AssertionResult holds the result of evaluating a single assertion.
//
// The Layer/Type/TraceNodePath/Expected/Actual/Judge fields are populated by
// the engine for diagnostic reports (report v2, pytest/vitest plugins).
// They are optional and absent in legacy v1 emissions.
type AssertionResult struct {
	AssertionID string  `json:"assertion_id"`
	Status      string  `json:"status"`
	Score       float64 `json:"score"`
	Explanation string  `json:"explanation"`
	Cost        float64 `json:"cost"`
	DurationMS  int64   `json:"duration_ms"`
	RequestID   string  `json:"request_id,omitempty"`
	// ThresholdSource records how Status was decided: "static" (default
	// fixed thresholds), "dynamic" (classified against historical
	// baseline), or "dynamic_unavailable" (dynamic classification was
	// requested but the baseline could not be loaded — Status reflects
	// the static fallback). Empty in legacy results.
	ThresholdSource string `json:"threshold_source,omitempty"`

	// Layer is the 1–8 pipeline layer derived from Type.
	Layer int `json:"layer,omitempty"`
	// Type is the assertion-type string (mirrors Assertion.Type) so reports
	// can group failures by type without re-joining against the request.
	Type string `json:"type,omitempty"`
	// TraceNodePath is a human-readable pointer into the trace structure
	// (e.g. "output", "steps[2].result", "agent.tools[0].llm_call") that
	// shows reviewers where the failure lives. Empty when the assertion
	// is not bound to a specific node.
	TraceNodePath string `json:"trace_node_path,omitempty"`
	// Expected describes the assertion's expectation in a form a human can
	// scan — for schema, the JSON-pointer path that failed validation; for
	// constraints, "<field> <op> <value>"; for content, the keyword set;
	// for embedding/judge, the threshold and reference.
	Expected string `json:"expected,omitempty"`
	// Actual carries the observed evidence the engine compared against
	// Expected. Truncated to ~512 bytes by the report writer.
	Actual string `json:"actual,omitempty"`
	// SuggestedAction is a short imperative hint the report renderer shows
	// next to the failure ("tighten threshold", "add missing tool",
	// "calibrate judge"). Optional.
	SuggestedAction string `json:"suggested_action,omitempty"`
	// Judge holds judge-specific audit metadata (model, rubric, variance
	// across repeated runs). Empty for non-judge assertions.
	Judge *JudgeMetadata `json:"judge_metadata,omitempty"`
}
