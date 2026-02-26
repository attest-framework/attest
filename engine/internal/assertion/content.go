package assertion

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// MaxRegexPatternLength is the maximum allowed length for regex patterns to prevent ReDoS.
const MaxRegexPatternLength = 10000

// ContentEvaluator implements Layer 4 content matching assertions.
type ContentEvaluator struct{}

func (e *ContentEvaluator) Evaluate(trace *types.Trace, assertion *types.Assertion) *types.AssertionResult {
	start := time.Now()

	var spec struct {
		Target        string   `json:"target"`
		Check         string   `json:"check"`
		Value         string   `json:"value,omitempty"`
		Values        []string `json:"values,omitempty"`
		Soft          bool     `json:"soft"`
		CaseSensitive bool     `json:"case_sensitive"`
	}
	if err := json.Unmarshal(assertion.Spec, &spec); err != nil {
		return failResult(assertion, start, fmt.Sprintf("invalid content spec: %v", err))
	}
	if spec.Target == "" {
		return failResult(assertion, start, "content spec missing required field: target")
	}
	if spec.Check == "" {
		return failResult(assertion, start, "content spec missing required field: check")
	}

	targetStr, err := ResolveTargetString(trace, spec.Target)
	if err != nil {
		return failResult(assertion, start, fmt.Sprintf("target resolution failed: %v", err))
	}

	compareTarget := targetStr
	compareValue := spec.Value
	if !spec.CaseSensitive {
		compareTarget = strings.ToLower(targetStr)
		compareValue = strings.ToLower(spec.Value)
	}

	failStatus := types.StatusHardFail
	if spec.Soft {
		failStatus = types.StatusSoftFail
	}

	switch spec.Check {
	case "contains":
		if strings.Contains(compareTarget, compareValue) {
			return passResult(assertion, start, fmt.Sprintf("%s contains '%s'.", spec.Target, spec.Value))
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: fmt.Sprintf("%s does not contain '%s'.", spec.Target, spec.Value),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	case "not_contains":
		if !strings.Contains(compareTarget, compareValue) {
			return passResult(assertion, start, fmt.Sprintf("%s does not contain '%s'.", spec.Target, spec.Value))
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: fmt.Sprintf("%s contains '%s' but should not.", spec.Target, spec.Value),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	case "regex_match":
		// E5: Reject patterns that exceed the length limit to prevent ReDoS.
		if len(spec.Value) > MaxRegexPatternLength {
			return failResult(assertion, start, fmt.Sprintf("regex pattern exceeds maximum length: %d > %d", len(spec.Value), MaxRegexPatternLength))
		}
		re, err := regexp.Compile(spec.Value)
		if err != nil {
			return failResult(assertion, start, fmt.Sprintf("invalid regex '%s': %v", spec.Value, err))
		}
		if re.MatchString(targetStr) {
			return passResult(assertion, start, fmt.Sprintf("%s matches regex '%s'.", spec.Target, spec.Value))
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: fmt.Sprintf("%s does not match regex '%s'.", spec.Target, spec.Value),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	case "keyword_all":
		missing := []string{}
		for _, kw := range spec.Values {
			cmpKW := kw
			if !spec.CaseSensitive {
				cmpKW = strings.ToLower(kw)
			}
			if !strings.Contains(compareTarget, cmpKW) {
				missing = append(missing, kw)
			}
		}
		if len(missing) == 0 {
			return passResult(assertion, start, fmt.Sprintf("%s contains all keywords.", spec.Target))
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       float64(len(spec.Values)-len(missing)) / float64(len(spec.Values)),
			Explanation: fmt.Sprintf("%s missing keywords: %v", spec.Target, missing),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	case "keyword_any":
		for _, kw := range spec.Values {
			cmpKW := kw
			if !spec.CaseSensitive {
				cmpKW = strings.ToLower(kw)
			}
			if strings.Contains(compareTarget, cmpKW) {
				return passResult(assertion, start, fmt.Sprintf("%s contains keyword '%s'.", spec.Target, kw))
			}
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      failStatus,
			Score:       0.0,
			Explanation: fmt.Sprintf("%s contains none of keywords: %v", spec.Target, spec.Values),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	case "forbidden":
		found := []string{}
		for _, kw := range spec.Values {
			cmpKW := kw
			if !spec.CaseSensitive {
				cmpKW = strings.ToLower(kw)
			}
			if strings.Contains(compareTarget, cmpKW) {
				found = append(found, kw)
			}
		}
		if len(found) == 0 {
			return passResult(assertion, start, fmt.Sprintf("%s contains none of forbidden terms.", spec.Target))
		}
		return &types.AssertionResult{
			AssertionID: assertion.AssertionID,
			Status:      types.StatusHardFail, // forbidden is always hard_fail
			Score:       0.0,
			Explanation: fmt.Sprintf("%s contains forbidden terms: %v", spec.Target, found),
			DurationMS:  time.Since(start).Milliseconds(),
			RequestID:   assertion.RequestID,
		}

	default:
		return failResult(assertion, start, fmt.Sprintf("unknown content check type: %s", spec.Check))
	}
}
