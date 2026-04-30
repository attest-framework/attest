package assertion

import (
	"fmt"
	"github.com/segmentio/encoding/json"
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

	d := diagFields{
		target:   spec.Target,
		expected: describeKeywordCheck(spec.Check, spec.Value, spec.Values),
		actual:   truncate(targetStr),
	}

	switch spec.Check {
	case "contains":
		if strings.Contains(compareTarget, compareValue) {
			return passResultWithDiag(assertion, start, fmt.Sprintf("%s contains '%s'.", spec.Target, spec.Value), d)
		}
		d.suggestion = fmt.Sprintf("Confirm the agent emits %q in %s, or remove the assertion if the contract changed.", spec.Value, spec.Target)
		return failResultWithDiag(assertion, start, failStatus, 0.0,
			fmt.Sprintf("%s does not contain '%s'.", spec.Target, spec.Value), d)

	case "not_contains":
		if !strings.Contains(compareTarget, compareValue) {
			return passResultWithDiag(assertion, start, fmt.Sprintf("%s does not contain '%s'.", spec.Target, spec.Value), d)
		}
		d.suggestion = fmt.Sprintf("Strip %q from %s before returning.", spec.Value, spec.Target)
		return failResultWithDiag(assertion, start, failStatus, 0.0,
			fmt.Sprintf("%s contains '%s' but should not.", spec.Target, spec.Value), d)

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
			return passResultWithDiag(assertion, start, fmt.Sprintf("%s matches regex '%s'.", spec.Target, spec.Value), d)
		}
		d.suggestion = "Test the regex on the actual output and adjust either the pattern or the agent's response shape."
		return failResultWithDiag(assertion, start, failStatus, 0.0,
			fmt.Sprintf("%s does not match regex '%s'.", spec.Target, spec.Value), d)

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
			return passResultWithDiag(assertion, start, fmt.Sprintf("%s contains all keywords.", spec.Target), d)
		}
		d.suggestion = fmt.Sprintf("Coverage gap: agent missed keywords %v. Update prompt or assertion.", missing)
		score := float64(len(spec.Values)-len(missing)) / float64(len(spec.Values))
		return failResultWithDiag(assertion, start, failStatus, score,
			fmt.Sprintf("%s missing keywords: %v", spec.Target, missing), d)

	case "keyword_any":
		for _, kw := range spec.Values {
			cmpKW := kw
			if !spec.CaseSensitive {
				cmpKW = strings.ToLower(kw)
			}
			if strings.Contains(compareTarget, cmpKW) {
				return passResultWithDiag(assertion, start, fmt.Sprintf("%s contains keyword '%s'.", spec.Target, kw), d)
			}
		}
		d.suggestion = "Agent answered without any of the required terms — review the system prompt."
		return failResultWithDiag(assertion, start, failStatus, 0.0,
			fmt.Sprintf("%s contains none of keywords: %v", spec.Target, spec.Values), d)

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
			return passResultWithDiag(assertion, start, fmt.Sprintf("%s contains none of forbidden terms.", spec.Target), d)
		}
		d.suggestion = fmt.Sprintf("Critical: forbidden terms %v leaked into %s. Likely safety regression.", found, spec.Target)
		// forbidden is always hard_fail regardless of soft flag.
		return failResultWithDiag(assertion, start, types.StatusHardFail, 0.0,
			fmt.Sprintf("%s contains forbidden terms: %v", spec.Target, found), d)

	default:
		return failResult(assertion, start, fmt.Sprintf("unknown content check type: %s", spec.Check))
	}
}

// passResultWithDiag is a passResult variant that carries diagnostic
// fields. Used by content checks so reports can show the matched value
// even on success when surfaced under --verbose.
func passResultWithDiag(assertion *types.Assertion, start time.Time, explanation string, d diagFields) *types.AssertionResult {
	return &types.AssertionResult{
		AssertionID:   assertion.AssertionID,
		Status:        types.StatusPass,
		Score:         1.0,
		Explanation:   explanation,
		DurationMS:    time.Since(start).Milliseconds(),
		RequestID:     assertion.RequestID,
		TraceNodePath: d.target,
		Expected:      d.expected,
		Actual:        d.actual,
	}
}
