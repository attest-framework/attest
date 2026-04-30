package types

import "testing"

func TestClassifyFailure_NilOrPassReturnsEmpty(t *testing.T) {
	if got := ClassifyFailure(nil); got != "" {
		t.Errorf("ClassifyFailure(nil) = %q, want \"\"", got)
	}
	pass := &AssertionResult{Status: StatusPass, Type: TypeLLMJudge}
	if got := ClassifyFailure(pass); got != "" {
		t.Errorf("ClassifyFailure(pass) = %q, want \"\"", got)
	}
}

func TestClassifyFailure_DynamicUnavailableIsMissingTraceData(t *testing.T) {
	r := &AssertionResult{
		Status:          StatusHardFail,
		Type:            TypeConstraint,
		Score:           0.0,
		ThresholdSource: ThresholdSourceDynamicUnavailable,
	}
	if got := ClassifyFailure(r); got != FailureClassMissingTraceData {
		t.Errorf("dynamic_unavailable hard_fail = %q, want %q", got, FailureClassMissingTraceData)
	}
}

func TestClassifyFailure_HighVarianceJudgeIsFlaky(t *testing.T) {
	r := &AssertionResult{
		Status: StatusSoftFail,
		Type:   TypeLLMJudge,
		Score:  0.6,
		Judge:  &JudgeMetadata{HighVarianceFlag: true},
	}
	if got := ClassifyFailure(r); got != FailureClassFlakyJudge {
		t.Errorf("high-variance judge = %q, want %q", got, FailureClassFlakyJudge)
	}
}

func TestClassifyFailure_LowKappaIsBadRubric(t *testing.T) {
	r := &AssertionResult{
		Status: StatusHardFail,
		Type:   TypeLLMJudge,
		Score:  0.2,
		Judge: &JudgeMetadata{
			Calibration: &JudgeAgreement{LabelCount: 50, CohenKappa: 0.15},
		},
	}
	if got := ClassifyFailure(r); got != FailureClassBadRubric {
		t.Errorf("low-kappa judge = %q, want %q", got, FailureClassBadRubric)
	}
}

func TestClassifyFailure_KappaAtThresholdIsNotBadRubric(t *testing.T) {
	// κ exactly at 0.4 should not be tagged bad_rubric — heuristic uses strict <.
	r := &AssertionResult{
		Status: StatusHardFail,
		Type:   TypeLLMJudge,
		Score:  0.3,
		Judge: &JudgeMetadata{
			Calibration: &JudgeAgreement{LabelCount: 50, CohenKappa: 0.4},
		},
	}
	if got := ClassifyFailure(r); got == FailureClassBadRubric {
		t.Errorf("κ=0.4 must not be bad_rubric; got %q", got)
	}
}

func TestClassifyFailure_NearBoundaryIsStochastic(t *testing.T) {
	// Embedding score 0.48, soft_fail, no judge metadata → stochastic.
	r := &AssertionResult{
		Status: StatusSoftFail,
		Type:   TypeEmbedding,
		Score:  0.48,
	}
	if got := ClassifyFailure(r); got != FailureClassStochasticVariance {
		t.Errorf("near-boundary embedding soft_fail = %q, want %q", got, FailureClassStochasticVariance)
	}
}

func TestClassifyFailure_DeterministicHardFailIsBrokenCode(t *testing.T) {
	cases := []struct {
		name      string
		assertion string
	}{
		{"schema", TypeSchema},
		{"constraint", TypeConstraint},
		{"trace", TypeTrace},
		{"trace_tree", TypeTraceTree},
		{"content", TypeContent},
		{"plugin", TypePlugin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &AssertionResult{
				Status: StatusHardFail,
				Type:   tc.assertion,
				Score:  0.0,
			}
			if got := ClassifyFailure(r); got != FailureClassBrokenCode {
				t.Errorf("%s hard_fail = %q, want %q", tc.assertion, got, FailureClassBrokenCode)
			}
		})
	}
}

func TestClassifyFailure_SoftFailDefaultsToStochastic(t *testing.T) {
	r := &AssertionResult{
		Status: StatusSoftFail,
		Type:   TypeContent,
		Score:  0.7,
	}
	if got := ClassifyFailure(r); got != FailureClassStochasticVariance {
		t.Errorf("soft_fail default = %q, want %q", got, FailureClassStochasticVariance)
	}
}

func TestClassifyFailure_PriorityHighVarianceOverLowKappa(t *testing.T) {
	// Both high variance AND low kappa — flaky judge wins because the
	// calibration result itself can't be trusted when scores are unstable.
	r := &AssertionResult{
		Status: StatusHardFail,
		Type:   TypeLLMJudge,
		Score:  0.2,
		Judge: &JudgeMetadata{
			HighVarianceFlag: true,
			Calibration:      &JudgeAgreement{LabelCount: 50, CohenKappa: 0.1},
		},
	}
	if got := ClassifyFailure(r); got != FailureClassFlakyJudge {
		t.Errorf("priority order broken: got %q, want %q", got, FailureClassFlakyJudge)
	}
}

func TestClassifyFailure_MissingTraceDataBeatsJudgeSignals(t *testing.T) {
	// dynamic_unavailable wins over judge-specific signals — the threshold
	// fallback means we cannot trust the comparison either way.
	r := &AssertionResult{
		Status:          StatusHardFail,
		Type:            TypeLLMJudge,
		Score:           0.3,
		ThresholdSource: ThresholdSourceDynamicUnavailable,
		Judge:           &JudgeMetadata{HighVarianceFlag: true},
	}
	if got := ClassifyFailure(r); got != FailureClassMissingTraceData {
		t.Errorf("dynamic_unavailable should beat flaky_judge: got %q", got)
	}
}
