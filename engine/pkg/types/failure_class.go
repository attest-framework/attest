package types

// FailureClass values describe the most likely root cause of a failed
// assertion. They drive the "what kind of failure is this?" column in
// reports and PR comments so reviewers can triage at a glance instead
// of having to read every explanation.
//
// Constants are stable strings: they are part of the JSON wire format
// (AssertionResult.FailureClass) and cross both SDKs. Empty string means
// "not classified" — used for passes and any result the heuristic
// declines to label.
const (
	// FailureClassBrokenCode means a deterministic layer (1–4 schema /
	// constraint / trace / content) hard-failed against an explicit
	// expectation. Likely root cause is application code: schema drift,
	// missing tool call, wrong field shape.
	FailureClassBrokenCode = "broken_code"

	// FailureClassFlakyJudge means an LLM-judge run reported high score
	// variance across repeated samples (JudgeMetadata.HighVarianceFlag).
	// Symptom: re-running the same eval flips pass/fail. Action: raise
	// repeat:N or tighten the rubric.
	FailureClassFlakyJudge = "flaky_judge"

	// FailureClassBadRubric means an LLM-judge run has a stored
	// calibration set with low agreement (Cohen κ < kappaPoorThreshold).
	// Action: rewrite the rubric or relabel the calibration set.
	FailureClassBadRubric = "bad_rubric"

	// FailureClassMissingTraceData means the assertion could not be
	// fairly evaluated because expected evidence was unavailable —
	// today this fires when ThresholdSource is dynamic_unavailable
	// (history baseline missing, so the result is the static fallback
	// rather than a true comparison).
	FailureClassMissingTraceData = "missing_trace_data"

	// FailureClassStochasticVariance means the score is close to the
	// pass/fail boundary on a probabilistic layer (5/6) and the failure
	// is plausibly noise. Action: raise the threshold or use repeat:N
	// to average out variance.
	FailureClassStochasticVariance = "stochastic_variance"
)

// kappaPoorThreshold is the Cohen κ value below which a calibration set
// is treated as evidence the rubric itself is the problem. Standard
// Landis & Koch buckets put 0.0–0.2 = "slight" and 0.2–0.4 = "fair";
// we draw the line at 0.4 ("moderate") so anything weaker triggers
// FailureClassBadRubric.
const kappaPoorThreshold = 0.4

// stochasticBoundaryEps is the score band around the soft/hard pass
// boundary inside which a probabilistic-layer failure is treated as
// stochastic variance rather than a clear miss.
const stochasticBoundaryEps = 0.05

// ClassifyFailure returns a FailureClass for r. Pure function — does
// not mutate r. Returns "" for pass results and for results the
// heuristic declines to label (rather than guessing wrong).
//
// Heuristic order, first match wins:
//
//  1. Pass status → ""
//  2. ThresholdSource == dynamic_unavailable → missing_trace_data
//     (the result reflects the static fallback, not a real comparison)
//  3. type == llm_judge AND Judge.HighVarianceFlag → flaky_judge
//  4. type == llm_judge AND Judge.Calibration with κ < 0.4 → bad_rubric
//  5. type ∈ {embedding, llm_judge} AND |score - 0.5| ≤ 0.05 → stochastic_variance
//  6. type ∈ {schema, constraint, trace, trace_tree, content, plugin}
//     hard_fail → broken_code
//  7. Any soft_fail not classified above → stochastic_variance
//     (this includes soft-fails on deterministic layers — a constraint
//     that soft-fails is "value just outside the threshold", which the
//     heuristic treats as borderline noise rather than broken code)
//  8. Anything else → broken_code (most conservative non-empty label)
func ClassifyFailure(r *AssertionResult) string {
	if r == nil || r.Status == StatusPass {
		return ""
	}

	if r.ThresholdSource == ThresholdSourceDynamicUnavailable {
		return FailureClassMissingTraceData
	}

	if r.Type == TypeLLMJudge && r.Judge != nil {
		if r.Judge.HighVarianceFlag {
			return FailureClassFlakyJudge
		}
		cal := r.Judge.Calibration
		if cal != nil && cal.LabelCount > 0 && cal.CohenKappa < kappaPoorThreshold {
			return FailureClassBadRubric
		}
	}

	if (r.Type == TypeEmbedding || r.Type == TypeLLMJudge) && nearBoundary(r.Score) {
		return FailureClassStochasticVariance
	}

	switch r.Type {
	case TypeSchema, TypeConstraint, TypeTrace, TypeTraceTree, TypeContent, TypePlugin:
		if r.Status == StatusHardFail {
			return FailureClassBrokenCode
		}
	}

	if r.Status == StatusSoftFail {
		return FailureClassStochasticVariance
	}
	return FailureClassBrokenCode
}

func nearBoundary(score float64) bool {
	d := score - 0.5
	if d < 0 {
		d = -d
	}
	return d <= stochasticBoundaryEps
}
