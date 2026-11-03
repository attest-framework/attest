package assertion

import (
	"fmt"
	"github.com/segmentio/encoding/json"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// TraceEvaluator implements Layer 3: trace structural inspection.
type TraceEvaluator struct{}

// traceSpec is the union of every trace check's fields. Each check reads the
// fields it needs and validates them in its runner.
type traceSpec struct {
	Check          string          `json:"check"`
	Tools          []string        `json:"tools,omitempty"`
	Tool           string          `json:"tool,omitempty"`
	MaxRepetitions int             `json:"max_repetitions,omitempty"`
	Per            string          `json:"per,omitempty"`
	Min            *int            `json:"min,omitempty"`
	Max            *int            `json:"max,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	Order          [][]string      `json:"order,omitempty"`
	Before         string          `json:"before,omitempty"`
	After          string          `json:"after,omitempty"`
	Period         int             `json:"period,omitempty"`
	MinRepeats     *int            `json:"min_repeats,omitempty"`
	Arg            string          `json:"arg,omitempty"`
	Op             string          `json:"op,omitempty"`
	Value          json.RawMessage `json:"value,omitempty"`
	Soft           bool            `json:"soft"`
}

// checkOutcome is a trace check's verdict. A non-empty missing marks the
// check not_evaluated(missing_evidence) with missing as the explanation.
type checkOutcome struct {
	passed      bool
	explanation string
	missing     string
}

// traceCheckRunner evaluates one check. A returned error is a spec error.
type traceCheckRunner func(trace *types.Trace, spec *traceSpec) (checkOutcome, error)

var traceChecks = map[string]traceCheckRunner{
	"contains_in_order": runContainsInOrder,
	"exact_order":       runExactOrder,
	"loop_detection":    runLoopDetection,
	"no_duplicates":     runNoDuplicates,
	"required_tools":    runRequiredTools,
	"forbidden_tools":   runForbiddenTools,
	"tool_cardinality":  runToolCardinality,
	"trajectory":        runTrajectory,
	"tool_order":        runToolOrder,
	"cycle":             runCycle,
	"failure_streak":    runFailureStreak,
	"arg_predicate":     runArgPredicate,
}

func (e *TraceEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec traceSpec
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid trace spec: %v", err))
	}
	if spec.Check == "" {
		return failResult(assertion, start, "trace spec missing required field: check")
	}
	run, ok := traceChecks[spec.Check]
	if !ok {
		return failResult(assertion, start, fmt.Sprintf("unsupported check type: %s", spec.Check))
	}
	outcome, err := run(trace, &spec)
	if err != nil {
		return failResult(assertion, start, err.Error())
	}
	if outcome.missing != "" {
		return notEvaluatedResult(assertion, start, types.ReasonMissingEvidence, outcome.missing)
	}
	if !outcome.passed {
		status := types.StatusHardFail
		if spec.Soft {
			status = types.StatusSoftFail
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      status,
			Score:       0.0,
			Explanation: outcome.explanation,
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}
	}
	return passResult(assertion, start, outcome.explanation)
}

// stepNames returns the name of every step, whatever its type. The original
// trace checks match on all steps; the behavior-contract checks match on
// tool calls only (see toolCalls).
func stepNames(trace *types.Trace) []string {
	names := make([]string, len(trace.Steps))
	for i, s := range trace.Steps {
		names[i] = s.Name
	}
	return names
}

func requireTools(spec *traceSpec) error {
	if len(spec.Tools) == 0 {
		return fmt.Errorf("%s requires 'tools'", spec.Check)
	}
	return nil
}

func outcomeOf(passed bool, explanation string) checkOutcome {
	return checkOutcome{passed: passed, explanation: explanation}
}

func runContainsInOrder(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if err := requireTools(spec); err != nil {
		return checkOutcome{}, err
	}
	return outcomeOf(checkContainsInOrder(stepNames(trace), spec.Tools)), nil
}

func runExactOrder(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if err := requireTools(spec); err != nil {
		return checkOutcome{}, err
	}
	return outcomeOf(checkExactOrder(stepNames(trace), spec.Tools)), nil
}

// runLoopDetection is tool_cardinality without a key over every step (not
// only tool calls), kept under its original name and fields.
func runLoopDetection(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if spec.Tool == "" {
		return checkOutcome{}, fmt.Errorf("loop_detection requires 'tool'")
	}
	if spec.MaxRepetitions <= 0 {
		return checkOutcome{}, fmt.Errorf("loop_detection requires 'max_repetitions' > 0")
	}
	maxN := spec.MaxRepetitions
	calls := indexSteps(trace, func(s *types.Step) bool { return s.Name == spec.Tool })
	return cardinality(spec.Tool, calls, "", nil, &maxN), nil
}

func runNoDuplicates(trace *types.Trace, _ *traceSpec) (checkOutcome, error) {
	return outcomeOf(checkNoDuplicates(stepNames(trace))), nil
}

func runRequiredTools(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if err := requireTools(spec); err != nil {
		return checkOutcome{}, err
	}
	return outcomeOf(checkRequiredTools(stepNames(trace), spec.Tools)), nil
}

func runForbiddenTools(trace *types.Trace, spec *traceSpec) (checkOutcome, error) {
	if err := requireTools(spec); err != nil {
		return checkOutcome{}, err
	}
	return outcomeOf(checkForbiddenTools(stepNames(trace), spec.Tools)), nil
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
