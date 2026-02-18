package assertion

import "github.com/attest-ai/attest/engine/pkg/types"

// BatchResult holds the results of evaluating a batch of assertions.
type BatchResult struct {
	Results         []types.AssertionResult
	TotalCost       float64
	TotalDurationMS int64
}

// ScoreThresholds defines pass and soft-fail score boundaries.
type ScoreThresholds struct {
	Pass float64
	Soft float64
}

// DefaultThresholds are the standard classification thresholds.
var DefaultThresholds = ScoreThresholds{Pass: 0.8, Soft: 0.5}

// ClassifyScore maps a score to a status string using default thresholds.
// score < 0.5  → hard_fail
// score < 0.8  → soft_fail
// score >= 0.8 → pass
func ClassifyScore(score float64) string {
	return ClassifyScoreWithThreshold(score, DefaultThresholds.Pass, DefaultThresholds.Soft)
}

// ClassifyScoreWithThreshold maps a score to a status string using provided thresholds.
// score < softThreshold  → hard_fail
// score < passThreshold  → soft_fail
// score >= passThreshold → pass
func ClassifyScoreWithThreshold(score, passThreshold, softThreshold float64) string {
	switch {
	case score >= passThreshold:
		return types.StatusPass
	case score >= softThreshold:
		return types.StatusSoftFail
	default:
		return types.StatusHardFail
	}
}
