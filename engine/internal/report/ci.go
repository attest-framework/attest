package report

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// Test outcomes in report-v2 `tests[].outcome`.
const (
	OutcomePassed  = "passed"
	OutcomeFailed  = "failed"
	OutcomeSkipped = "skipped"
)

// CIReplaySummary is `summary.replay`: the agent's own model calls served from
// fixtures (replayed) and sent to a provider (live).
type CIReplaySummary struct {
	Replayed          int     `json:"replayed"`
	Live              int     `json:"live"`
	Misses            int     `json:"misses"`
	LiveCostUSD       float64 `json:"live_cost_usd"`
	UnpricedLiveCalls int     `json:"unpriced_live_calls"`
}

// CITestReplay is `tests[].replay`.
type CITestReplay struct {
	Fixtures []string `json:"fixtures"`
	Mode     string   `json:"mode"`
	Replayed int      `json:"replayed"`
	Live     int      `json:"live"`
	Misses   int      `json:"misses"`
	Miss     string   `json:"miss,omitempty"`
}

// CIResult is the part of a report-v2 `results[]` row the CI outputs read,
// including the test context the SDKs add.
type CIResult struct {
	AssertionID  string `json:"assertion_id"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Explanation  string `json:"explanation,omitempty"`
	Type         string `json:"type,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	TestID       string `json:"test_id,omitempty"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`

	// Fields read by the failure viewer.
	Cost          float64 `json:"cost,omitempty"`
	DurationMS    int64   `json:"duration_ms,omitempty"`
	Layer         int     `json:"layer,omitempty"`
	TraceNodePath string  `json:"trace_node_path,omitempty"`
	Expected      string  `json:"expected,omitempty"`
	Actual        string  `json:"actual,omitempty"`
	TraceID       string  `json:"trace_id,omitempty"`

	// src is the index of the report the row came from in MergeCI; it keeps
	// rows of different reports apart when their test ids collide.
	src int
}

// CITest is a report-v2 `tests[]` row.
type CITest struct {
	TestID              string        `json:"test_id"`
	Name                string        `json:"name"`
	File                string        `json:"file"`
	Line                int           `json:"line"`
	Outcome             string        `json:"outcome"`
	NotEvaluatedReasons []string      `json:"not_evaluated_reasons,omitempty"`
	DurationMS          int64         `json:"duration_ms,omitempty"`
	Replay              *CITestReplay `json:"replay,omitempty"`

	src int
}

// CISummary is the report-v2 `summary` block.
type CISummary struct {
	Total        int              `json:"total"`
	Passed       int              `json:"passed"`
	SoftFail     int              `json:"soft_fail"`
	HardFail     int              `json:"hard_fail"`
	NotEvaluated int              `json:"not_evaluated"`
	Usage        *types.Usage     `json:"usage,omitempty"`
	Replay       *CIReplaySummary `json:"replay,omitempty"`
}

// CIReport is the subset of a report-v2 document the JUnit writer, the
// summary renderer and the annotation rows read. Several reports merge with
// MergeCI.
type CIReport struct {
	ReportVersion int        `json:"report_version"`
	Summary       CISummary  `json:"summary"`
	Results       []CIResult `json:"results"`
	Tests         []CITest   `json:"tests"`

	// Fields read by the failure viewer. Traces is present only when the run
	// set ATTEST_REPORT_TRACES=1; after MergeCI its keys are unique across the
	// merged reports and every row's TraceID names its entry.
	Simulated       bool                       `json:"simulated,omitempty"`
	TotalCost       float64                    `json:"total_cost,omitempty"`
	TotalDurationMS int64                      `json:"total_duration_ms,omitempty"`
	Traces          map[string]json.RawMessage `json:"traces,omitempty"`
}

// LoadCI reads one report-v2 file. A missing file, malformed JSON or another
// report version is an error naming the path.
func LoadCI(path string) (*CIReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	var r CIReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode report %s: %w", path, err)
	}
	if r.ReportVersion != 2 {
		return nil, fmt.Errorf("report %s is version %d; expected 2", path, r.ReportVersion)
	}
	return &r, nil
}

// MergeCI concatenates reports in the order given and sums their summaries.
func MergeCI(reports []*CIReport) *CIReport {
	merged := &CIReport{ReportVersion: 2}
	for i, r := range reports {
		merged.TotalCost += r.TotalCost
		merged.TotalDurationMS += r.TotalDurationMS
		merged.Simulated = merged.Simulated || r.Simulated
		for _, res := range r.Results {
			res.src = i
			if res.TraceID != "" && len(reports) > 1 {
				res.TraceID = fmt.Sprintf("r%d:%s", i+1, res.TraceID)
			}
			merged.Results = append(merged.Results, res)
		}
		for _, t := range r.Tests {
			t.src = i
			merged.Tests = append(merged.Tests, t)
		}
		for id, raw := range r.Traces {
			if merged.Traces == nil {
				merged.Traces = map[string]json.RawMessage{}
			}
			if len(reports) > 1 {
				id = fmt.Sprintf("r%d:%s", i+1, id)
			}
			merged.Traces[id] = raw
		}
		s := &merged.Summary
		s.Total += r.Summary.Total
		s.Passed += r.Summary.Passed
		s.SoftFail += r.Summary.SoftFail
		s.HardFail += r.Summary.HardFail
		s.NotEvaluated += r.Summary.NotEvaluated
		if r.Summary.Usage != nil {
			if s.Usage == nil {
				s.Usage = &types.Usage{}
			}
			s.Usage.Add(*r.Summary.Usage)
		}
		if r.Summary.Replay != nil {
			if s.Replay == nil {
				s.Replay = &CIReplaySummary{}
			}
			s.Replay.Replayed += r.Summary.Replay.Replayed
			s.Replay.Live += r.Summary.Replay.Live
			s.Replay.Misses += r.Summary.Replay.Misses
			s.Replay.LiveCostUSD += r.Summary.Replay.LiveCostUSD
			s.Replay.UnpricedLiveCalls += r.Summary.Replay.UnpricedLiveCalls
		}
	}
	return merged
}

// LoadMergedCI loads every path and merges the reports.
func LoadMergedCI(paths []string) (*CIReport, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one report is required")
	}
	reports := make([]*CIReport, 0, len(paths))
	for _, p := range paths {
		r, err := LoadCI(p)
		if err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return MergeCI(reports), nil
}

// FindingRow is one failure or replay miss with its source location. It is
// the row shape of `attest-engine report rows --json`.
type FindingRow struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	TestID      string `json:"test_id"`
	AssertionID string `json:"assertion_id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	Explanation string `json:"explanation"`
}

// Row types for findings whose assertion row carries none.
const (
	replayRowType    = "replay"
	assertionRowType = "assertion"
)

// testKey identifies a test within a merged report: test ids can repeat across
// the reports that were merged.
type testKey struct {
	src int
	id  string
}

// notEvaluatedSkips returns the skipped tests that count as not evaluated
// under contract C1: the test has at least one assertion row and every row is
// not_evaluated. Any other skipped test (a plain skip with no rows, or an
// expected failure whose rows carry another status) is merely skipped.
func notEvaluatedSkips(r *CIReport) map[testKey]bool {
	statuses := map[testKey][]string{}
	for _, res := range r.Results {
		k := testKey{res.src, res.TestID}
		statuses[k] = append(statuses[k], res.Status)
	}
	out := map[testKey]bool{}
	for _, t := range r.Tests {
		if t.Outcome != OutcomeSkipped {
			continue
		}
		k := testKey{t.src, t.TestID}
		rows := statuses[k]
		all := len(rows) > 0
		for _, st := range rows {
			if st != types.StatusNotEvaluated {
				all = false
			}
		}
		out[k] = all
	}
	return out
}

// FindingRows returns the failures and replay misses of r, sorted by file,
// line, test id and assertion id. A row is a hard failure or a not_evaluated
// result whose reason is replay_miss. Rows of a skipped test that is not an
// all-not_evaluated skip are dropped (an expected failure is not a finding), as
// is a hard_fail row of a passed test: a test can pass with failing trials
// when a minimum pass rate is set. A strict-mode replay miss fails its test and the SDKs
// write no assertion row for it, so a failed test whose replay block holds a
// miss message and has no replay_miss row yet contributes a hard_fail row with
// reason replay_miss. A row from a result without test context has an empty
// file and line 0.
func FindingRows(r *CIReport) []FindingRow {
	skips := notEvaluatedSkips(r)
	outcomes := map[testKey]string{}
	for _, t := range r.Tests {
		outcomes[testKey{t.src, t.TestID}] = t.Outcome
	}
	var rows []FindingRow
	missRows := map[testKey]bool{}
	for _, res := range r.Results {
		isFail := res.Status == types.StatusHardFail
		isMiss := res.Status == types.StatusNotEvaluated && res.Reason == types.ReasonReplayMiss
		if !isFail && !isMiss {
			continue
		}
		k := testKey{res.src, res.TestID}
		if allNE, skipped := skips[k]; skipped && !allNE {
			continue
		}
		if outcome, known := outcomes[k]; isFail && known && outcome != OutcomeFailed {
			continue
		}
		kind := res.Type
		if kind == "" {
			kind = assertionRowType
			if isMiss {
				kind = replayRowType
			}
		}
		if isMiss {
			missRows[testKey{res.src, res.TestID}] = true
		}
		rows = append(rows, FindingRow{
			File: res.File, Line: res.Line, TestID: res.TestID,
			AssertionID: res.AssertionID, Type: kind, Status: res.Status,
			Reason: res.Reason, Explanation: res.Explanation,
		})
	}
	for _, t := range r.Tests {
		rp := t.Replay
		if t.Outcome != OutcomeFailed || rp == nil || rp.Miss == "" || len(rp.Fixtures) == 0 || missRows[testKey{t.src, t.TestID}] {
			continue
		}
		rows = append(rows, FindingRow{
			File: t.File, Line: t.Line, TestID: t.TestID, AssertionID: "replay_" + rp.Fixtures[0],
			Type: replayRowType, Status: types.StatusHardFail,
			Reason: types.ReasonReplayMiss, Explanation: rp.Miss,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.TestID != b.TestID {
			return a.TestID < b.TestID
		}
		return a.AssertionID < b.AssertionID
	})
	return rows
}
