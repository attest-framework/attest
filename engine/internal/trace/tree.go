package trace

import (
	"fmt"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// TraceVisitor is called for each trace in the tree during a walk.
// Return false to stop walking.
type TraceVisitor func(t *types.Trace, depth int) bool

// WalkTree performs a depth-first walk of the trace tree, calling visitor for each trace.
func WalkTree(root *types.Trace, visitor TraceVisitor) {
	walkTreeAtDepth(root, 0, visitor)
}

func walkTreeAtDepth(t *types.Trace, depth int, visitor TraceVisitor) {
	if !visitor(t, depth) {
		return
	}
	for i := range t.Steps {
		if t.Steps[i].Type == types.StepTypeAgentCall && t.Steps[i].SubTrace != nil {
			walkTreeAtDepth(t.Steps[i].SubTrace, depth+1, visitor)
		}
	}
}

// CollectSubTraces returns all traces in the tree in depth-first order, including root.
func CollectSubTraces(root *types.Trace) []*types.Trace {
	var result []*types.Trace
	WalkTree(root, func(t *types.Trace, _ int) bool {
		result = append(result, t)
		return true
	})
	return result
}

// FindAgentByID finds the first trace with the given AgentID, or nil if not found.
func FindAgentByID(root *types.Trace, agentID string) *types.Trace {
	var found *types.Trace
	WalkTree(root, func(t *types.Trace, _ int) bool {
		if t.AgentID == agentID {
			found = t
			return false
		}
		return true
	})
	return found
}

// TreeDepth returns the maximum nesting depth of the trace tree (root = 0).
func TreeDepth(root *types.Trace) int {
	maxDepth := 0
	WalkTree(root, func(_ *types.Trace, depth int) bool {
		if depth > maxDepth {
			maxDepth = depth
		}
		return true
	})
	return maxDepth
}

// AgentIDs returns all agent IDs present in the trace tree.
func AgentIDs(root *types.Trace) []string {
	var ids []string
	WalkTree(root, func(t *types.Trace, _ int) bool {
		if t.AgentID != "" {
			ids = append(ids, t.AgentID)
		}
		return true
	})
	return ids
}

// ValidateTraceTree validates the structural integrity of a trace tree.
// It checks for:
//   - agent_call steps must have sub_traces
//   - parent_trace_id consistency (child's parent_trace_id must match parent's trace_id)
//   - no duplicate trace_ids (cycle detection)
//   - nesting depth within MaxSubTraceDepth
func ValidateTraceTree(root *types.Trace) error {
	seen := make(map[string]struct{})
	return validateTreeAtDepth(root, nil, 0, seen)
}

func validateTreeAtDepth(t *types.Trace, parent *types.Trace, depth int, seen map[string]struct{}) error {
	if depth > MaxSubTraceDepth {
		return fmt.Errorf("trace nesting depth %d exceeds maximum %d", depth, MaxSubTraceDepth)
	}

	if _, exists := seen[t.TraceID]; exists {
		return fmt.Errorf("duplicate trace_id: cycle detected at %q", t.TraceID)
	}
	seen[t.TraceID] = struct{}{}

	if parent != nil && t.ParentTraceID != nil && *t.ParentTraceID != parent.TraceID {
		return fmt.Errorf("sub_trace %q has parent_trace_id %q but parent trace_id is %q", t.TraceID, *t.ParentTraceID, parent.TraceID)
	}

	for i := range t.Steps {
		step := &t.Steps[i]
		if step.Type == types.StepTypeAgentCall && step.SubTrace == nil {
			return fmt.Errorf("agent_call step %q in trace %q is missing sub_trace", step.Name, t.TraceID)
		}
		if step.Type == types.StepTypeAgentCall && step.SubTrace != nil {
			if err := validateTreeAtDepth(step.SubTrace, t, depth+1, seen); err != nil {
				return err
			}
		}
	}

	return nil
}

// AggregateMetadata computes aggregate metrics across the entire trace tree.
// Returns total tokens, total cost in USD, total latency in ms, and agent count.
func AggregateMetadata(root *types.Trace) (totalTokens int, totalCostUSD float64, totalLatencyMS int, agentCount int) {
	WalkTree(root, func(t *types.Trace, _ int) bool {
		agentCount++
		if t.Metadata != nil {
			if t.Metadata.TotalTokens != nil {
				totalTokens += *t.Metadata.TotalTokens
			}
			if t.Metadata.CostUSD != nil {
				totalCostUSD += *t.Metadata.CostUSD
			}
			if t.Metadata.LatencyMS != nil {
				totalLatencyMS += *t.Metadata.LatencyMS
			}
		}
		return true
	})
	return
}
