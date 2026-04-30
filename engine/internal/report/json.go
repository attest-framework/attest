package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/segmentio/encoding/json"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// ReportVersion identifies the JSON report schema. v1 is the legacy flat
// shape kept for one minor release; v2 adds layer/expected/actual/judge
// fields and a layer-grouped index. Callers select the version via
// GenerateJSONReport's options.
type ReportVersion int

const (
	ReportVersionV1 ReportVersion = 1
	ReportVersionV2 ReportVersion = 2
)

// JSONReport is the legacy v1 envelope. New consumers should read v2
// (JSONReportV2). Retained verbatim so existing tooling does not break.
type JSONReport struct {
	Version       string                  `json:"version"`
	Timestamp     string                  `json:"timestamp"`
	Results       []types.AssertionResult `json:"results"`
	Summary       JSONSummary             `json:"summary"`
	TotalCost     float64                 `json:"total_cost"`
	TotalDuration int64                   `json:"total_duration_ms"`
}

type JSONSummary struct {
	Total    int `json:"total"`
	Passed   int `json:"passed"`
	SoftFail int `json:"soft_fail"`
	HardFail int `json:"hard_fail"`
}

// JSONReportV2 is the diagnostic-rich envelope. AssertionResult already
// carries the new fields (Layer, Type, Expected, Actual, Judge) via
// JSON-omitempty, so the change is purely additive at the row level.
//
// Top-level additions:
//   - report_version: integer (2)
//   - simulated: bool — true when produced by an SDK simulation shim
//   - layers: per-layer counts and assertion id lists
//   - p50_duration_ms / p95_duration_ms: latency percentiles
type JSONReportV2 struct {
	ReportVersion   int                     `json:"report_version"`
	Timestamp       string                  `json:"timestamp"`
	Simulated       bool                    `json:"simulated"`
	Summary         JSONSummary             `json:"summary"`
	Layers          []LayerSummary          `json:"layers"`
	Results         []types.AssertionResult `json:"results"`
	TotalCost       float64                 `json:"total_cost"`
	TotalDurationMS int64                   `json:"total_duration_ms"`
	P50DurationMS   int64                   `json:"p50_duration_ms"`
	P95DurationMS   int64                   `json:"p95_duration_ms"`
}

// LayerSummary is the per-layer index emitted in v2.
type LayerSummary struct {
	Layer        int      `json:"layer"`
	Name         string   `json:"name"`
	Total        int      `json:"total"`
	Passed       int      `json:"passed"`
	SoftFail     int      `json:"soft_fail"`
	HardFail     int      `json:"hard_fail"`
	AssertionIDs []string `json:"assertion_ids"`
}

// ReportOptions configures JSON report generation. Zero value emits v2.
type ReportOptions struct {
	Version   ReportVersion
	Simulated bool
}

// GenerateJSONReport emits a JSON report. By default it returns v2. Pass
// ReportOptions{Version: ReportVersionV1} to emit the legacy schema for
// one minor release.
func GenerateJSONReport(results []types.AssertionResult, totalCost float64, totalDurationMS int64, opts ...ReportOptions) ([]byte, error) {
	var opt ReportOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.Version == ReportVersionV1 {
		return generateJSONV1(results, totalCost, totalDurationMS)
	}
	return generateJSONV2(results, totalCost, totalDurationMS, opt)
}

func generateJSONV1(results []types.AssertionResult, totalCost float64, totalDurationMS int64) ([]byte, error) {
	summary := buildSummary(results)
	if totalCost == 0 {
		totalCost = sumCost(results)
	}
	report := JSONReport{
		Version:       "1.0",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Results:       results,
		Summary:       summary,
		TotalCost:     totalCost,
		TotalDuration: totalDurationMS,
	}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON v1: %w", err)
	}
	return output, nil
}

func generateJSONV2(results []types.AssertionResult, totalCost float64, totalDurationMS int64, opt ReportOptions) ([]byte, error) {
	summary := buildSummary(results)
	if totalCost == 0 {
		totalCost = sumCost(results)
	}
	layers := buildLayerSummaries(results)
	p50, p95 := durationPercentiles(results)

	report := JSONReportV2{
		ReportVersion:   int(ReportVersionV2),
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Simulated:       opt.Simulated,
		Summary:         summary,
		Layers:          layers,
		Results:         results,
		TotalCost:       totalCost,
		TotalDurationMS: totalDurationMS,
		P50DurationMS:   p50,
		P95DurationMS:   p95,
	}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON v2: %w", err)
	}
	return output, nil
}

func buildSummary(results []types.AssertionResult) JSONSummary {
	s := JSONSummary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case types.StatusPass:
			s.Passed++
		case types.StatusSoftFail:
			s.SoftFail++
		case types.StatusHardFail:
			s.HardFail++
		}
	}
	return s
}

func sumCost(results []types.AssertionResult) float64 {
	var total float64
	for _, r := range results {
		total += r.Cost
	}
	return total
}

func buildLayerSummaries(results []types.AssertionResult) []LayerSummary {
	groups := make(map[int][]types.AssertionResult)
	for _, r := range results {
		layer := r.Layer
		if layer == 0 {
			layer = types.LayerForType(r.Type)
		}
		groups[layer] = append(groups[layer], r)
	}

	out := make([]LayerSummary, 0, len(groups))
	for layer, group := range groups {
		ls := LayerSummary{
			Layer:        layer,
			Name:         layerName(layer),
			Total:        len(group),
			AssertionIDs: make([]string, 0, len(group)),
		}
		for _, r := range group {
			switch r.Status {
			case types.StatusPass:
				ls.Passed++
			case types.StatusSoftFail:
				ls.SoftFail++
			case types.StatusHardFail:
				ls.HardFail++
			}
			ls.AssertionIDs = append(ls.AssertionIDs, r.AssertionID)
		}
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Layer < out[j].Layer })
	return out
}

func durationPercentiles(results []types.AssertionResult) (p50, p95 int64) {
	durations := make([]int64, 0, len(results))
	for _, r := range results {
		if r.DurationMS > 0 {
			durations = append(durations, r.DurationMS)
		}
	}
	if len(durations) == 0 {
		return 0, 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return percentile(durations, 50), percentile(durations, 95)
}
