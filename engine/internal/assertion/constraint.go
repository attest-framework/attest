package assertion

import (
	"github.com/segmentio/encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// stepTypeFilterRegex matches steps[?type=='<type>'].length
var stepTypeFilterRegex = regexp.MustCompile(`^steps\[\?type=='([^']+)'\]\.length$`)

// ConstraintEvaluator implements Layer 2: numeric constraint checks.
type ConstraintEvaluator struct{}

func (e *ConstraintEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec struct {
		Field    string   `json:"field"`
		Operator string   `json:"operator"`
		Value    *float64 `json:"value,omitempty"`
		Min      *float64 `json:"min,omitempty"`
		Max      *float64 `json:"max,omitempty"`
		Soft     bool     `json:"soft"`
	}
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid constraint spec: %v", err))
	}
	if spec.Field == "" {
		return failResult(assertion, start, "constraint spec missing required field: field")
	}
	if spec.Operator == "" {
		return failResult(assertion, start, "constraint spec missing required field: operator")
	}

	actualVal, err := resolveConstraintField(trace, spec.Field)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("field resolution failed: %v", err))
	}

	failStatus := types.StatusHardFail
	if spec.Soft {
		failStatus = types.StatusSoftFail
	}

	var passed bool
	var explanation string

	switch spec.Operator {
	case "lt":
		if spec.Value == nil {
			return failResult(assertion, start, "operator 'lt' requires 'value'")
		}
		passed = actualVal < *spec.Value
		explanation = fmt.Sprintf("%s = %s, constraint lt %s", spec.Field, formatFloat(actualVal), formatFloat(*spec.Value))
	case "lte":
		if spec.Value == nil {
			return failResult(assertion, start, "operator 'lte' requires 'value'")
		}
		passed = actualVal <= *spec.Value
		explanation = fmt.Sprintf("%s = %s, constraint lte %s", spec.Field, formatFloat(actualVal), formatFloat(*spec.Value))
	case "gt":
		if spec.Value == nil {
			return failResult(assertion, start, "operator 'gt' requires 'value'")
		}
		passed = actualVal > *spec.Value
		explanation = fmt.Sprintf("%s = %s, constraint gt %s", spec.Field, formatFloat(actualVal), formatFloat(*spec.Value))
	case "gte":
		if spec.Value == nil {
			return failResult(assertion, start, "operator 'gte' requires 'value'")
		}
		passed = actualVal >= *spec.Value
		explanation = fmt.Sprintf("%s = %s, constraint gte %s", spec.Field, formatFloat(actualVal), formatFloat(*spec.Value))
	case "eq":
		if spec.Value == nil {
			return failResult(assertion, start, "operator 'eq' requires 'value'")
		}
		passed = actualVal == *spec.Value
		explanation = fmt.Sprintf("%s = %s, constraint eq %s", spec.Field, formatFloat(actualVal), formatFloat(*spec.Value))
	case "between":
		if spec.Min == nil || spec.Max == nil {
			return failResult(assertion, start, "operator 'between' requires 'min' and 'max'")
		}
		passed = actualVal >= *spec.Min && actualVal <= *spec.Max
		explanation = fmt.Sprintf("%s = %s, constraint between [%s, %s]", spec.Field, formatFloat(actualVal), formatFloat(*spec.Min), formatFloat(*spec.Max))
	default:
		return failResult(assertion, start, fmt.Sprintf("unsupported operator: %s", spec.Operator))
	}

	if !passed {
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: explanation + " — constraint not satisfied.",
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}
	}

	return &types.AssertionResult{
		AssertionID: assertion.AssertionID,
		Status:      types.StatusPass,
		Score:       1.0,
		Explanation: explanation + " — satisfied.",
		DurationMS:  time.Since(start).Milliseconds(),
		RequestID:   assertion.RequestID,
	}
}

// resolveConstraintField resolves a constraint field path to a float64 value.
func resolveConstraintField(trace *types.Trace, field string) (float64, error) {
	switch field {
	case "metadata.cost_usd":
		if trace.Metadata == nil || trace.Metadata.CostUSD == nil {
			return 0, fmt.Errorf("metadata.cost_usd is not set")
		}
		return *trace.Metadata.CostUSD, nil

	case "metadata.total_tokens":
		if trace.Metadata == nil || trace.Metadata.TotalTokens == nil {
			return 0, fmt.Errorf("metadata.total_tokens is not set")
		}
		return float64(*trace.Metadata.TotalTokens), nil

	case "metadata.latency_ms":
		if trace.Metadata == nil || trace.Metadata.LatencyMS == nil {
			return 0, fmt.Errorf("metadata.latency_ms is not set")
		}
		return float64(*trace.Metadata.LatencyMS), nil

	case "steps.length":
		return float64(len(trace.Steps)), nil
	}

	// steps[?type=='<type>'].length
	if m := stepTypeFilterRegex.FindStringSubmatch(field); m != nil {
		stepType := m[1]
		count := 0
		for _, s := range trace.Steps {
			if s.Type == stepType {
				count++
			}
		}
		return float64(count), nil
	}

	return 0, fmt.Errorf("unsupported constraint field: %s", field)
}

// formatFloat formats a float64 for display, trimming trailing zeros.
func formatFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}
