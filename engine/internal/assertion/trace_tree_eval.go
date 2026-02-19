package assertion

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// TraceTreeEvaluator implements cross-agent trace tree assertions.
type TraceTreeEvaluator struct{}

func (e *TraceTreeEvaluator) Evaluate(t *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var base struct {
		Check string `json:"check"`
		Soft  bool   `json:"soft"`
	}
	if err := json.Unmarshal(assertion.Spec, &base); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid trace_tree spec: %v", err))
	}
	if base.Check == "" {
		return failResult(assertion, start, "trace_tree spec missing required field: check")
	}

	failStatus := types.StatusHardFail
	if base.Soft {
		failStatus = types.StatusSoftFail
	}

	var passed bool
	var explanation string

	switch base.Check {
	case "agent_called":
		passed, explanation = checkAgentCalled(t, assertion.Spec)
	case "delegation_depth":
		passed, explanation = checkDelegationDepth(t, assertion.Spec)
	case "agent_output_contains":
		passed, explanation = checkAgentOutputContains(t, assertion.Spec)
	case "cross_agent_data_flow":
		passed, explanation = checkCrossAgentDataFlow(t, assertion.Spec)
	case "aggregate_cost":
		passed, explanation = checkAggregateCostCheck(t, assertion.Spec)
	case "aggregate_tokens":
		passed, explanation = checkAggregateTokensCheck(t, assertion.Spec)
	case "aggregate_latency":
		passed, explanation = checkAggregateLatencyCheck(t, assertion.Spec)
	default:
		return failResult(assertion, start, fmt.Sprintf("unsupported trace_tree check: %s", base.Check))
	}

	status := types.StatusPass
	score := 1.0
	if !passed {
		status = failStatus
		score = 0.0
	}

	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      status,
		Score:       score,
		Explanation: explanation,
		DurationMS:  time.Since(start).Milliseconds(),
		RequestID:   assertion.RequestID,
	}
}

func checkAgentCalled(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("agent_called: invalid spec: %v", err)
	}
	if s.AgentID == "" {
		return false, "agent_called requires 'agent_id'"
	}
	found := trace.FindAgentByID(t, s.AgentID)
	if found == nil {
		return false, fmt.Sprintf("agent %q was not called in the trace tree", s.AgentID)
	}
	return true, fmt.Sprintf("agent %q was called in the trace tree.", s.AgentID)
}

func checkDelegationDepth(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		MaxDepth int `json:"max_depth"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("delegation_depth: invalid spec: %v", err)
	}
	if s.MaxDepth <= 0 {
		return false, "delegation_depth requires 'max_depth' > 0"
	}
	depth := trace.TreeDepth(t)
	if depth > s.MaxDepth {
		return false, fmt.Sprintf("delegation depth %d exceeds max_depth %d", depth, s.MaxDepth)
	}
	return true, fmt.Sprintf("delegation depth %d is within max_depth %d.", depth, s.MaxDepth)
}

func checkAgentOutputContains(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		AgentID       string `json:"agent_id"`
		Value         string `json:"value"`
		CaseSensitive bool   `json:"case_sensitive"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("agent_output_contains: invalid spec: %v", err)
	}
	if s.AgentID == "" {
		return false, "agent_output_contains requires 'agent_id'"
	}
	if s.Value == "" {
		return false, "agent_output_contains requires 'value'"
	}

	found := trace.FindAgentByID(t, s.AgentID)
	if found == nil {
		return false, fmt.Sprintf("agent %q not found in trace tree", s.AgentID)
	}

	outputStr := string(found.Output)
	needle := s.Value
	haystack := outputStr
	if !s.CaseSensitive {
		needle = strings.ToLower(needle)
		haystack = strings.ToLower(haystack)
	}
	if !strings.Contains(haystack, needle) {
		return false, fmt.Sprintf("agent %q output does not contain %q", s.AgentID, s.Value)
	}
	return true, fmt.Sprintf("agent %q output contains %q.", s.AgentID, s.Value)
}

func checkCrossAgentDataFlow(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		FromAgent string `json:"from_agent"`
		ToAgent   string `json:"to_agent"`
		Field     string `json:"field"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("cross_agent_data_flow: invalid spec: %v", err)
	}
	if s.FromAgent == "" || s.ToAgent == "" || s.Field == "" {
		return false, "cross_agent_data_flow requires 'from_agent', 'to_agent', and 'field'"
	}

	fromTrace := trace.FindAgentByID(t, s.FromAgent)
	if fromTrace == nil {
		return false, fmt.Sprintf("from_agent %q not found in trace tree", s.FromAgent)
	}
	toTrace := trace.FindAgentByID(t, s.ToAgent)
	if toTrace == nil {
		return false, fmt.Sprintf("to_agent %q not found in trace tree", s.ToAgent)
	}

	var fromOutput map[string]interface{}
	if err := json.Unmarshal(fromTrace.Output, &fromOutput); err != nil {
		return false, fmt.Sprintf("from_agent %q output is not a JSON object: %v", s.FromAgent, err)
	}

	fieldVal, ok := fromOutput[s.Field]
	if !ok {
		return false, fmt.Sprintf("field %q not found in from_agent %q output", s.Field, s.FromAgent)
	}

	// Serialize the field value and check it appears in to_agent input.
	fieldBytes, err := json.Marshal(fieldVal)
	if err != nil {
		return false, fmt.Sprintf("failed to marshal field %q value: %v", s.Field, err)
	}

	toInputStr := string(toTrace.Input)
	// Strip quotes from scalar JSON values for substring matching.
	fieldStr := strings.Trim(string(fieldBytes), "\"")
	if !strings.Contains(toInputStr, fieldStr) {
		return false, fmt.Sprintf("field %q value from agent %q not found in agent %q input", s.Field, s.FromAgent, s.ToAgent)
	}
	return true, fmt.Sprintf("field %q flows from agent %q to agent %q.", s.Field, s.FromAgent, s.ToAgent)
}

func checkAggregateCostCheck(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		Operator string  `json:"operator"`
		Value    float64 `json:"value"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("aggregate_cost: invalid spec: %v", err)
	}
	if s.Operator == "" {
		return false, "aggregate_cost requires 'operator'"
	}
	_, totalCostUSD, _, _ := trace.AggregateMetadata(t)
	return applyNumericOperator("aggregate_cost", totalCostUSD, s.Operator, s.Value)
}

func checkAggregateTokensCheck(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		Operator string  `json:"operator"`
		Value    float64 `json:"value"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("aggregate_tokens: invalid spec: %v", err)
	}
	if s.Operator == "" {
		return false, "aggregate_tokens requires 'operator'"
	}
	totalTokens, _, _, _ := trace.AggregateMetadata(t)
	return applyNumericOperator("aggregate_tokens", float64(totalTokens), s.Operator, s.Value)
}

func checkAggregateLatencyCheck(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		Operator string  `json:"operator"`
		Value    float64 `json:"value"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("aggregate_latency: invalid spec: %v", err)
	}
	if s.Operator == "" {
		return false, "aggregate_latency requires 'operator'"
	}
	_, _, totalLatencyMS, _ := trace.AggregateMetadata(t)
	return applyNumericOperator("aggregate_latency", float64(totalLatencyMS), s.Operator, s.Value)
}

// applyNumericOperator evaluates actual op threshold and returns pass/fail with explanation.
// Supported operators: lte, gte, eq, lt, gt.
func applyNumericOperator(checkName string, actual float64, operator string, threshold float64) (bool, string) {
	var passed bool
	switch operator {
	case "lte":
		passed = actual <= threshold
	case "gte":
		passed = actual >= threshold
	case "eq":
		passed = actual == threshold
	case "lt":
		passed = actual < threshold
	case "gt":
		passed = actual > threshold
	default:
		return false, fmt.Sprintf("%s: unsupported operator %q (use lte, gte, eq, lt, gt)", checkName, operator)
	}
	if !passed {
		return false, fmt.Sprintf("%s: actual %.4g %s threshold %.4g failed", checkName, actual, operator, threshold)
	}
	return true, fmt.Sprintf("%s: actual %.4g %s threshold %.4g passed.", checkName, actual, operator, threshold)
}
