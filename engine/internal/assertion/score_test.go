package assertion_test

import (
	"testing"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func TestClassifyScore(t *testing.T) {
	cases := []struct {
		score    float64
		expected string
	}{
		{0.0, types.StatusHardFail},
		{0.49, types.StatusHardFail},
		{0.5, types.StatusSoftFail},
		{0.79, types.StatusSoftFail},
		{0.8, types.StatusPass},
		{1.0, types.StatusPass},
	}

	for _, tc := range cases {
		got := assertion.ClassifyScore(tc.score)
		if got != tc.expected {
			t.Errorf("ClassifyScore(%f) = %q, want %q", tc.score, got, tc.expected)
		}
	}
}

func TestClassifyScoreWithThreshold(t *testing.T) {
	cases := []struct {
		score         float64
		passThreshold float64
		softThreshold float64
		expected      string
	}{
		{0.3, 0.9, 0.6, types.StatusHardFail},
		{0.7, 0.9, 0.6, types.StatusSoftFail},
		{0.95, 0.9, 0.6, types.StatusPass},
		// Boundary: exactly at soft threshold
		{0.6, 0.9, 0.6, types.StatusSoftFail},
		// Boundary: exactly at pass threshold
		{0.9, 0.9, 0.6, types.StatusPass},
	}

	for _, tc := range cases {
		got := assertion.ClassifyScoreWithThreshold(tc.score, tc.passThreshold, tc.softThreshold)
		if got != tc.expected {
			t.Errorf("ClassifyScoreWithThreshold(%f, %f, %f) = %q, want %q",
				tc.score, tc.passThreshold, tc.softThreshold, got, tc.expected)
		}
	}
}

func TestDefaultThresholds(t *testing.T) {
	if assertion.DefaultThresholds.Pass != 0.8 {
		t.Errorf("DefaultThresholds.Pass = %f, want 0.8", assertion.DefaultThresholds.Pass)
	}
	if assertion.DefaultThresholds.Soft != 0.5 {
		t.Errorf("DefaultThresholds.Soft = %f, want 0.5", assertion.DefaultThresholds.Soft)
	}
}

func uniform(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestClassifyDynamic_BelowMinRunsUsesStaticThreshold(t *testing.T) {
	cfg := assertion.DefaultDynamicConfig
	// Three entries are below MinRuns, so only the static threshold applies.
	history := []float64{0.9, 0.85, 0.88}

	if got := assertion.ClassifyDynamic(0.85, history, 0.8, cfg); got != types.StatusPass {
		t.Errorf("0.85 against static 0.8 = %q, want pass", got)
	}
	if got := assertion.ClassifyDynamic(0.6, history, 0.8, cfg); got != types.StatusSoftFail {
		t.Errorf("0.6 against static 0.8 = %q, want soft_fail", got)
	}
	if got := assertion.ClassifyDynamic(0.9, []float64{}, 0.8, cfg); got != types.StatusPass {
		t.Errorf("0.9 with empty history = %q, want pass", got)
	}
}

func TestClassifyDynamic_ScoreBelowBarHardFails(t *testing.T) {
	// mean 0.9, sigma floor 0.02, k=2 -> bar 0.86
	history := uniform(10, 0.9)

	if got := assertion.ClassifyDynamic(0.85, history, 0.8, assertion.DefaultDynamicConfig); got != types.StatusHardFail {
		t.Errorf("0.85 against bar 0.86 = %q, want hard_fail", got)
	}
	if got := assertion.ClassifyDynamic(0.87, history, 0.8, assertion.DefaultDynamicConfig); got != types.StatusPass {
		t.Errorf("0.87 against bar 0.86 = %q, want pass", got)
	}
}

func TestClassifyDynamic_SigmaFloorToleratesSteadyHistory(t *testing.T) {
	// Zero observed sigma: the 0.02 floor gives a 0.04 tolerance below 1.0.
	history := uniform(10, 1.0)

	if got := assertion.ClassifyDynamic(0.99, history, 0.8, assertion.DefaultDynamicConfig); got != types.StatusPass {
		t.Errorf("0.99 against steady 1.0 = %q, want pass", got)
	}
	if got := assertion.ClassifyDynamic(0.95, history, 0.8, assertion.DefaultDynamicConfig); got != types.StatusHardFail {
		t.Errorf("0.95 against steady 1.0 = %q, want hard_fail", got)
	}
}

func TestClassifyDynamic_BarNeverBelowStaticThreshold(t *testing.T) {
	// A weak baseline (mean 0.3) must not lower the bar under the static 0.8.
	history := uniform(10, 0.3)

	if got := assertion.ClassifyDynamic(0.3, history, 0.8, assertion.DefaultDynamicConfig); got != types.StatusHardFail {
		t.Errorf("0.3 against weak baseline = %q, want hard_fail", got)
	}
	if got := assertion.ClassifyDynamic(0.0, uniform(10, 0.0), 0.8, assertion.DefaultDynamicConfig); got != types.StatusHardFail {
		t.Errorf("0.0 against all-zero baseline = %q, want hard_fail", got)
	}
}

func TestComputeBaseline_UsesOnlyTheNewestWindow(t *testing.T) {
	// 50 newest scores at 1.0, then 20 older at 0.0: the window excludes the zeros.
	history := append(uniform(50, 1.0), uniform(20, 0.0)...)

	b := assertion.ComputeBaseline(history, 0.8, assertion.DefaultDynamicConfig)

	if b.Count != 50 || b.Mean != 1.0 || !b.Active {
		t.Errorf("baseline = %+v, want count 50 mean 1.0 active", b)
	}
}
