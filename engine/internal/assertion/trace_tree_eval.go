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
	case "follows_transitions":
		passed, explanation = checkFollowsTransitions(t, assertion.Spec)
	case "aggregate_latency":
		passed, explanation = checkAggregateLatencyCheck(t, assertion.Spec)
	case "agent_ordered_before":
		passed, explanation = checkAgentOrderedBefore(t, assertion.Spec)
	case "agents_overlap":
		passed, explanation = checkAgentsOverlap(t, assertion.Spec)
	case "agent_wall_time_under":
		passed, explanation = checkAgentWallTimeUnder(t, assertion.Spec)
	case "ordered_agents":
		passed, explanation = checkOrderedAgents(t, assertion.Spec)
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

func checkFollowsTransitions(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		Transitions [][]string `json:"transitions"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("follows_transitions: invalid spec: %v", err)
	}
	if len(s.Transitions) == 0 {
		return false, "follows_transitions requires non-empty 'transitions'"
	}

	// Build allowed set from transitions spec.
	type pair struct{ parent, child string }
	allowed := make(map[pair]struct{}, len(s.Transitions))
	for _, tr := range s.Transitions {
		if len(tr) != 2 {
			return false, fmt.Sprintf("follows_transitions: each transition must be [parent, child], got %v", tr)
		}
		allowed[pair{tr[0], tr[1]}] = struct{}{}
	}

	// Collect actual delegation pairs from the trace tree.
	var violations []string
	var collectDelegations func(t *types.Trace)
	collectDelegations = func(t *types.Trace) {
		parentID := t.AgentID
		for i := range t.Steps {
			step := &t.Steps[i]
			if step.Type == types.StepTypeAgentCall && step.SubTrace != nil {
				childID := step.SubTrace.AgentID
				p := pair{parentID, childID}
				if _, ok := allowed[p]; !ok {
					violations = append(violations, fmt.Sprintf("%s -> %s", parentID, childID))
				}
				collectDelegations(step.SubTrace)
			}
		}
	}
	collectDelegations(t)

	if len(violations) > 0 {
		return false, fmt.Sprintf("follows_transitions: disallowed delegation(s): %s", strings.Join(violations, ", "))
	}
	return true, "follows_transitions: all delegations match allowed transitions."
}

func checkAgentOrderedBefore(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		AgentA string `json:"agent_a"`
		AgentB string `json:"agent_b"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("agent_ordered_before: invalid spec: %v", err)
	}
	if s.AgentA == "" || s.AgentB == "" {
		return false, "agent_ordered_before requires 'agent_a' and 'agent_b'"
	}

	stepsA := trace.CollectStepsByAgentID(t, s.AgentA)
	stepsB := trace.CollectStepsByAgentID(t, s.AgentB)

	if len(stepsA) == 0 {
		return false, fmt.Sprintf("agent_ordered_before: no steps found for agent_a %q", s.AgentA)
	}
	if len(stepsB) == 0 {
		return false, fmt.Sprintf("agent_ordered_before: no steps found for agent_b %q", s.AgentB)
	}

	// Find last ended_at_ms for agent_a.
	var lastEndedA int64 = -1
	for _, step := range stepsA {
		if step.EndedAtMs == nil {
			return false, fmt.Sprintf("agent_ordered_before: step for agent_a %q missing ended_at_ms", s.AgentA)
		}
		if *step.EndedAtMs > lastEndedA {
			lastEndedA = *step.EndedAtMs
		}
	}

	// Find first started_at_ms for agent_b.
	var firstStartedB int64 = -1
	for _, step := range stepsB {
		if step.StartedAtMs == nil {
			return false, fmt.Sprintf("agent_ordered_before: step for agent_b %q missing started_at_ms", s.AgentB)
		}
		if firstStartedB == -1 || *step.StartedAtMs < firstStartedB {
			firstStartedB = *step.StartedAtMs
		}
	}

	if lastEndedA >= firstStartedB {
		return false, fmt.Sprintf("agent_ordered_before: agent_a %q last ended at %d ms, agent_b %q first started at %d ms — not strictly before", s.AgentA, lastEndedA, s.AgentB, firstStartedB)
	}
	return true, fmt.Sprintf("agent_ordered_before: agent_a %q (last ended %d ms) completed before agent_b %q (first started %d ms).", s.AgentA, lastEndedA, s.AgentB, firstStartedB)
}

func checkAgentsOverlap(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		AgentA string `json:"agent_a"`
		AgentB string `json:"agent_b"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("agents_overlap: invalid spec: %v", err)
	}
	if s.AgentA == "" || s.AgentB == "" {
		return false, "agents_overlap requires 'agent_a' and 'agent_b'"
	}

	stepsA := trace.CollectStepsByAgentID(t, s.AgentA)
	stepsB := trace.CollectStepsByAgentID(t, s.AgentB)

	if len(stepsA) == 0 {
		return false, fmt.Sprintf("agents_overlap: no steps found for agent_a %q", s.AgentA)
	}
	if len(stepsB) == 0 {
		return false, fmt.Sprintf("agents_overlap: no steps found for agent_b %q", s.AgentB)
	}

	// Find the bounding interval for each agent across all its steps.
	var minStartA, maxEndA int64 = -1, -1
	for _, step := range stepsA {
		if step.StartedAtMs == nil || step.EndedAtMs == nil {
			return false, fmt.Sprintf("agents_overlap: step for agent_a %q missing temporal fields", s.AgentA)
		}
		if minStartA == -1 || *step.StartedAtMs < minStartA {
			minStartA = *step.StartedAtMs
		}
		if *step.EndedAtMs > maxEndA {
			maxEndA = *step.EndedAtMs
		}
	}

	var minStartB, maxEndB int64 = -1, -1
	for _, step := range stepsB {
		if step.StartedAtMs == nil || step.EndedAtMs == nil {
			return false, fmt.Sprintf("agents_overlap: step for agent_b %q missing temporal fields", s.AgentB)
		}
		if minStartB == -1 || *step.StartedAtMs < minStartB {
			minStartB = *step.StartedAtMs
		}
		if *step.EndedAtMs > maxEndB {
			maxEndB = *step.EndedAtMs
		}
	}

	overlaps := minStartA < maxEndB && minStartB < maxEndA
	if !overlaps {
		return false, fmt.Sprintf("agents_overlap: agent_a %q [%d, %d] and agent_b %q [%d, %d] do not overlap", s.AgentA, minStartA, maxEndA, s.AgentB, minStartB, maxEndB)
	}
	return true, fmt.Sprintf("agents_overlap: agent_a %q [%d, %d] and agent_b %q [%d, %d] overlap.", s.AgentA, minStartA, maxEndA, s.AgentB, minStartB, maxEndB)
}

func checkAgentWallTimeUnder(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		AgentID string  `json:"agent_id"`
		MaxMS   float64 `json:"max_ms"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("agent_wall_time_under: invalid spec: %v", err)
	}
	if s.AgentID == "" {
		return false, "agent_wall_time_under requires 'agent_id'"
	}
	if s.MaxMS <= 0 {
		return false, "agent_wall_time_under requires 'max_ms' > 0"
	}

	steps := trace.CollectStepsByAgentID(t, s.AgentID)
	if len(steps) == 0 {
		return false, fmt.Sprintf("agent_wall_time_under: no steps found for agent_id %q", s.AgentID)
	}

	var totalMS int64
	for _, step := range steps {
		if step.StartedAtMs == nil || step.EndedAtMs == nil {
			return false, fmt.Sprintf("agent_wall_time_under: step for agent_id %q missing temporal fields", s.AgentID)
		}
		totalMS += *step.EndedAtMs - *step.StartedAtMs
	}

	if float64(totalMS) >= s.MaxMS {
		return false, fmt.Sprintf("agent_wall_time_under: agent %q total wall time %d ms >= max_ms %.4g", s.AgentID, totalMS, s.MaxMS)
	}
	return true, fmt.Sprintf("agent_wall_time_under: agent %q total wall time %d ms < max_ms %.4g.", s.AgentID, totalMS, s.MaxMS)
}

func checkOrderedAgents(t *types.Trace, spec json.RawMessage) (bool, string) {
	var s struct {
		Groups [][]string `json:"groups"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return false, fmt.Sprintf("ordered_agents: invalid spec: %v", err)
	}
	if len(s.Groups) < 2 {
		return false, "ordered_agents requires at least 2 groups"
	}

	// For each group, compute max ended_at_ms (all agents in group must complete).
	// For each consecutive pair, max ended of group[i] < min started of group[i+1].
	type groupBounds struct {
		maxEnded   int64
		minStarted int64
	}

	bounds := make([]groupBounds, len(s.Groups))
	for gi, group := range s.Groups {
		if len(group) == 0 {
			return false, fmt.Sprintf("ordered_agents: group %d is empty", gi)
		}
		var maxEnded int64 = -1
		var minStarted int64 = -1
		for _, agentID := range group {
			steps := trace.CollectStepsByAgentID(t, agentID)
			if len(steps) == 0 {
				return false, fmt.Sprintf("ordered_agents: no steps found for agent %q in group %d", agentID, gi)
			}
			for _, step := range steps {
				if step.StartedAtMs == nil || step.EndedAtMs == nil {
					return false, fmt.Sprintf("ordered_agents: step for agent %q missing temporal fields", agentID)
				}
				if minStarted == -1 || *step.StartedAtMs < minStarted {
					minStarted = *step.StartedAtMs
				}
				if *step.EndedAtMs > maxEnded {
					maxEnded = *step.EndedAtMs
				}
			}
		}
		bounds[gi] = groupBounds{maxEnded: maxEnded, minStarted: minStarted}
	}

	for i := 0; i < len(bounds)-1; i++ {
		if bounds[i].maxEnded >= bounds[i+1].minStarted {
			return false, fmt.Sprintf("ordered_agents: group %d max ended (%d ms) is not before group %d min started (%d ms)", i, bounds[i].maxEnded, i+1, bounds[i+1].minStarted)
		}
	}
	return true, fmt.Sprintf("ordered_agents: all %d groups are sequentially ordered.", len(s.Groups))
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
