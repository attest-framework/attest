package assertion_test

import (
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/pkg/types"
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

func TestClassifyDynamic_FallbackWhenBelowMinRuns(t *testing.T) {
	cfg := assertion.DynamicConfig{WindowSize: 50, SigmaScale: 2.0, MinRuns: 10}
	// Only 3 history entries — below MinRuns=10, so falls back to ClassifyScore.
	history := []float64{0.9, 0.85, 0.88}

	cases := []struct {
		score    float64
		expected string
	}{
		{0.3, types.StatusHardFail},  // ClassifyScore: < 0.5
		{0.6, types.StatusSoftFail},  // ClassifyScore: 0.5 <= x < 0.8
		{0.85, types.StatusPass},     // ClassifyScore: >= 0.8
	}

	for _, tc := range cases {
		got := assertion.ClassifyDynamic(tc.score, history, cfg)
		if got != tc.expected {
			t.Errorf("ClassifyDynamic(%f, history[3], fallback) = %q, want %q", tc.score, got, tc.expected)
		}
	}
}

func TestClassifyDynamic_PassAboveThreshold(t *testing.T) {
	cfg := assertion.DynamicConfig{WindowSize: 50, SigmaScale: 2.0, MinRuns: 5}
	// mean=0.8, stddev=0 → threshold=0.8; score 0.85 >= 0.8 → pass
	history := []float64{0.8, 0.8, 0.8, 0.8, 0.8}

	got := assertion.ClassifyDynamic(0.85, history, cfg)
	if got != types.StatusPass {
		t.Errorf("ClassifyDynamic(0.85, uniform-0.8 history) = %q, want %q", got, types.StatusPass)
	}
}

func TestClassifyDynamic_FailBelowThreshold(t *testing.T) {
	cfg := assertion.DynamicConfig{WindowSize: 50, SigmaScale: 1.0, MinRuns: 5}
	// mean=0.8, stddev=0 → threshold=0.8; score 0.75 < 0.8 → hard_fail
	history := []float64{0.8, 0.8, 0.8, 0.8, 0.8}

	got := assertion.ClassifyDynamic(0.75, history, cfg)
	if got != types.StatusHardFail {
		t.Errorf("ClassifyDynamic(0.75, uniform-0.8 history) = %q, want %q", got, types.StatusHardFail)
	}
}

func TestClassifyDynamic_ZeroStddevUniformHistory(t *testing.T) {
	cfg := assertion.DynamicConfig{WindowSize: 50, SigmaScale: 2.0, MinRuns: 5}
	// All same scores: mean=0.7, stddev=0, threshold=0.7-0=0.7
	history := []float64{0.7, 0.7, 0.7, 0.7, 0.7}

	// Score exactly at mean: pass
	if got := assertion.ClassifyDynamic(0.7, history, cfg); got != types.StatusPass {
		t.Errorf("ClassifyDynamic(0.7, uniform-0.7) = %q, want pass", got)
	}
	// Score below mean: hard_fail
	if got := assertion.ClassifyDynamic(0.69, history, cfg); got != types.StatusHardFail {
		t.Errorf("ClassifyDynamic(0.69, uniform-0.7) = %q, want hard_fail", got)
	}
}

func TestClassifyDynamic_EmptyHistory(t *testing.T) {
	cfg := assertion.DynamicConfig{WindowSize: 50, SigmaScale: 2.0, MinRuns: 5}
	// Empty history — len(0) < MinRuns(5), falls back to ClassifyScore.
	got := assertion.ClassifyDynamic(0.9, []float64{}, cfg)
	if got != types.StatusPass {
		t.Errorf("ClassifyDynamic(0.9, empty) = %q, want pass", got)
	}
}
