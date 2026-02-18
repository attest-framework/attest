package trace

import "github.com/attest-ai/attest/engine/pkg/types"

// StepsByType returns all steps with the given type.
func StepsByType(t *types.Trace, stepType string) []types.Step {
	result := make([]types.Step, 0)
	for _, s := range t.Steps {
		if s.Type == stepType {
			result = append(result, s)
		}
	}
	return result
}

// StepByName returns the first step with the given name, or nil if not found.
func StepByName(t *types.Trace, name string) *types.Step {
	for i := range t.Steps {
		if t.Steps[i].Name == name {
			return &t.Steps[i]
		}
	}
	return nil
}

// StepCount returns the total number of steps in the trace.
func StepCount(t *types.Trace) int {
	return len(t.Steps)
}

// ToolCallCount returns the number of steps with type "tool_call".
func ToolCallCount(t *types.Trace) int {
	return len(StepsByType(t, types.StepTypeToolCall))
}
