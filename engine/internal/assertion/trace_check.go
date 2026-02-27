package assertion

import (
	"github.com/segmentio/encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// TraceEvaluator implements Layer 3: trace structural inspection.
type TraceEvaluator struct{}

func (e *TraceEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec struct {
		Check          string   `json:"check"`
		Tools          []string `json:"tools,omitempty"`
		Tool           string   `json:"tool,omitempty"`
		MaxRepetitions int      `json:"max_repetitions,omitempty"`
		Soft           bool     `json:"soft"`
	}
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid trace spec: %v", err))
	}
	if spec.Check == "" {
		return failResult(assertion, start, "trace spec missing required field: check")
	}

	failStatus := types.StatusHardFail
	if spec.Soft {
		failStatus = types.StatusSoftFail
	}

	stepNames := make([]string, len(trace.Steps))
	for i, s := range trace.Steps {
		stepNames[i] = s.Name
	}

	var explanation string
	var passed bool

	switch spec.Check {
	case "contains_in_order":
		if len(spec.Tools) == 0 {
			return failResult(assertion, start, "contains_in_order requires 'tools'")
		}
		passed, explanation = checkContainsInOrder(stepNames, spec.Tools)

	case "exact_order":
		if len(spec.Tools) == 0 {
			return failResult(assertion, start, "exact_order requires 'tools'")
		}
		passed, explanation = checkExactOrder(stepNames, spec.Tools)

	case "loop_detection":
		if spec.Tool == "" {
			return failResult(assertion, start, "loop_detection requires 'tool'")
		}
		if spec.MaxRepetitions <= 0 {
			return failResult(assertion, start, "loop_detection requires 'max_repetitions' > 0")
		}
		passed, explanation = checkLoopDetection(stepNames, spec.Tool, spec.MaxRepetitions)

	case "no_duplicates":
		passed, explanation = checkNoDuplicates(stepNames)

	case "required_tools":
		if len(spec.Tools) == 0 {
			return failResult(assertion, start, "required_tools requires 'tools'")
		}
		passed, explanation = checkRequiredTools(stepNames, spec.Tools)

	case "forbidden_tools":
		if len(spec.Tools) == 0 {
			return failResult(assertion, start, "forbidden_tools requires 'tools'")
		}
		passed, explanation = checkForbiddenTools(stepNames, spec.Tools)

	default:
		return failResult(assertion, start, fmt.Sprintf("unsupported check type: %s", spec.Check))
	}

	if !passed {
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: explanation,
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}
	}

	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      types.StatusPass,
		Score:       1.0,
		Explanation: explanation,
		DurationMS:  time.Since(start).Milliseconds(),
		RequestID:   assertion.RequestID,
	}
}

// checkContainsInOrder verifies that tools appear in stepNames in order (non-contiguously).
func checkContainsInOrder(stepNames []string, tools []string) (bool, string) {
	indices := make([]int, 0, len(tools))
	cursor := 0
	for _, tool := range tools {
		found := false
		for i := cursor; i < len(stepNames); i++ {
			if stepNames[i] == tool {
				indices = append(indices, i)
				cursor = i + 1
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("tool sequence %v not found in order; missing %q after position %d", tools, tool, cursor)
		}
	}
	return true, fmt.Sprintf("tool sequence %v found in order at steps %v.", tools, indices)
}

// checkExactOrder verifies that tools appear contiguously in exact order with no other steps between them.
func checkExactOrder(stepNames []string, tools []string) (bool, string) {
	if len(tools) > len(stepNames) {
		return false, fmt.Sprintf("exact order %v not found: trace has fewer steps than required sequence", tools)
	}

	for start := 0; start <= len(stepNames)-len(tools); start++ {
		match := true
		for j, tool := range tools {
			if stepNames[start+j] != tool {
				match = false
				break
			}
		}
		if match {
			indices := make([]int, len(tools))
			for j := range tools {
				indices[j] = start + j
			}
			return true, fmt.Sprintf("tool sequence %v found in exact order at steps %v.", tools, indices)
		}
	}
	return false, fmt.Sprintf("tool sequence %v not found in exact contiguous order", tools)
}

// checkLoopDetection verifies that a specific tool does not appear more than maxRepetitions times.
func checkLoopDetection(stepNames []string, tool string, maxRepetitions int) (bool, string) {
	count := 0
	for _, name := range stepNames {
		if name == tool {
			count++
		}
	}
	if count > maxRepetitions {
		return false, fmt.Sprintf("tool %q called %d times, exceeds max_repetitions %d", tool, count, maxRepetitions)
	}
	return true, fmt.Sprintf("tool %q called %d times, within max_repetitions %d.", tool, count, maxRepetitions)
}

// checkNoDuplicates verifies that no step name appears more than once.
func checkNoDuplicates(stepNames []string) (bool, string) {
	seen := make(map[string]int)
	for _, name := range stepNames {
		seen[name]++
	}
	var duplicates []string
	for name, count := range seen {
		if count > 1 {
			duplicates = append(duplicates, fmt.Sprintf("%q (%d times)", name, count))
		}
	}
	if len(duplicates) > 0 {
		return false, fmt.Sprintf("duplicate step names found: %s", strings.Join(duplicates, ", "))
	}
	return true, "no duplicate step names found."
}

// checkRequiredTools verifies that all listed tools appear at least once.
func checkRequiredTools(stepNames []string, tools []string) (bool, string) {
	nameSet := make(map[string]bool, len(stepNames))
	for _, name := range stepNames {
		nameSet[name] = true
	}
	var missing []string
	for _, tool := range tools {
		if !nameSet[tool] {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("required tools not found in trace: %v", missing)
	}
	return true, fmt.Sprintf("all required tools found: %v.", tools)
}

// checkForbiddenTools verifies that none of the listed tools appear in the trace.
func checkForbiddenTools(stepNames []string, tools []string) (bool, string) {
	nameSet := make(map[string]bool, len(stepNames))
	for _, name := range stepNames {
		nameSet[name] = true
	}
	var found []string
	for _, tool := range tools {
		if nameSet[tool] {
			found = append(found, tool)
		}
	}
	if len(found) > 0 {
		return false, fmt.Sprintf("forbidden tools found in trace: %v", found)
	}
	return true, fmt.Sprintf("none of the forbidden tools %v found in trace.", tools)
}
