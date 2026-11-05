package assertion

import (
	"math"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// BatchResult holds the results of evaluating a batch of assertions.
type BatchResult struct {
	Results         []types.AssertionResult
	TotalCost       float64
	TotalDurationMS int64
	// BudgetExceeded is set when the soft-fail budget was breached during the
	// batch. The batch still evaluates every assertion.
	BudgetExceeded bool
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

// DynamicConfig holds the drift estimator parameters (contract C6): one
// estimator serves classification, query_drift and result.drift.
type DynamicConfig struct {
	// WindowSize is the number of most recent passing or soft scores used.
	WindowSize int
	// SigmaScale is k in the bar mean − k·σ.
	SigmaScale float64
	// SigmaFloor is the smallest σ used, so a perfectly steady history does
	// not turn measurement noise into a failure.
	SigmaFloor float64
	// MinRuns is the number of baseline scores required before the history
	// replaces the static threshold.
	MinRuns int
}

// DefaultDynamicConfig is the C6 estimator: window 50, k=2, σ floor 0.02.
var DefaultDynamicConfig = DynamicConfig{WindowSize: 50, SigmaScale: 2.0, SigmaFloor: 0.02, MinRuns: 10}

// DynamicBaseline is the estimator output for one assertion.
type DynamicBaseline struct {
	Mean   float64
	Stddev float64 // raw population σ of the window, before the floor
	Count  int     // baseline scores used
	Bar    float64 // pass bar: max(static, mean − k·max(σ, floor))
	Active bool    // Count >= MinRuns; otherwise only the static threshold applies
}

// ComputeBaseline applies the C6 estimator to history, newest score first.
// The caller supplies passing and soft scores only: a failure must never
// become the bar. The bar is never below the static threshold.
func ComputeBaseline(history []float64, static float64, cfg DynamicConfig) DynamicBaseline {
	if len(history) > cfg.WindowSize {
		history = history[:cfg.WindowSize]
	}
	mean, stddev := computeStats(history)
	sigma := math.Max(stddev, cfg.SigmaFloor)
	return DynamicBaseline{
		Mean:   mean,
		Stddev: stddev,
		Count:  len(history),
		Bar:    math.Max(static, mean-cfg.SigmaScale*sigma),
		Active: len(history) >= cfg.MinRuns,
	}
}

// ClassifyDynamic classifies a score against a historical baseline of passing
// and soft scores (newest first). Below cfg.MinRuns it uses the static
// threshold alone; otherwise a score below the bar is a hard_fail. The bar
// never falls under static.
func ClassifyDynamic(score float64, history []float64, static float64, cfg DynamicConfig) string {
	b := ComputeBaseline(history, static, cfg)
	if !b.Active {
		return ClassifyScoreWithThreshold(score, static, DefaultThresholds.Soft)
	}
	if score >= b.Bar {
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
