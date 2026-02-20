package assertion

import (
	"math"

	"github.com/attest-ai/attest/engine/pkg/types"
)

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

// DynamicConfig holds parameters for dynamic threshold classification.
type DynamicConfig struct {
	WindowSize int
	SigmaScale float64
	MinRuns    int
}

// DefaultDynamicConfig provides sensible defaults for dynamic classification.
var DefaultDynamicConfig = DynamicConfig{WindowSize: 50, SigmaScale: 2.0, MinRuns: 10}

// ClassifyDynamic classifies a score using a historical baseline.
// When len(history) < cfg.MinRuns, falls back to ClassifyScore.
// Otherwise: pass if score >= mean - sigmaScale*stddev, hard_fail otherwise.
func ClassifyDynamic(score float64, history []float64, cfg DynamicConfig) string {
	if len(history) < cfg.MinRuns {
		return ClassifyScore(score)
	}
	mean, stddev := computeStats(history)
	threshold := mean - cfg.SigmaScale*stddev
	if score >= threshold {
		return types.StatusPass
	}
	return types.StatusHardFail
}

// computeStats returns the mean and population standard deviation of data.
func computeStats(data []float64) (mean, stddev float64) {
	if len(data) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	mean = sum / float64(len(data))

	sumSqDiff := 0.0
	for _, v := range data {
		diff := v - mean
		sumSqDiff += diff * diff
	}
	stddev = math.Sqrt(sumSqDiff / float64(len(data)))
	return mean, stddev
}
