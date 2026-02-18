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
