package report_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/report"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadFixture(t *testing.T, names ...string) *report.CIReport {
	t.Helper()
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join("testdata", "ci", n)
	}
	r, err := report.LoadMergedCI(paths)
	if err != nil {
		t.Fatalf("LoadMergedCI: %v", err)
	}
	return r
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "ci", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func TestRenderSummary_Mixed_MatchesGolden(t *testing.T) {
	policy := &report.PolicyOutcome{
		ExitCode: 1,
		Violations: []report.PolicyViolation{
			{Rule: "hard_fail", Detail: "1 hard failure", Severity: "block"},
		},
	}
	got := report.RenderSummary(loadFixture(t, "mixed.json"), policy)
	golden(t, "summary_mixed.md", got)
}

func TestRenderSummary_Clean_ShowsZeroLiveCalls(t *testing.T) {
	got := report.RenderSummary(loadFixture(t, "clean.json"), nil)
	golden(t, "summary_clean.md", got)
	if !strings.Contains(got, "3 replayed · 0 live model calls · $0.00") {
		t.Errorf("summary lacks the replay line:\n%s", got)
	}
}

func TestRenderSummary_IdenticalInputs_AreByteIdentical(t *testing.T) {
	a := report.RenderSummary(loadFixture(t, "mixed.json", "clean.json"), nil)
	b := report.RenderSummary(loadFixture(t, "mixed.json", "clean.json"), nil)
	if a != b {
		t.Error("summary differs between identical runs")
	}
}

func TestRenderSummary_MoreThanCapRows_ReportsRemainder(t *testing.T) {
	r := &report.CIReport{ReportVersion: 2}
	for i := 0; i < 25; i++ {
		r.Results = append(r.Results, report.CIResult{
			AssertionID: "a", Status: "hard_fail", Type: "effect", Explanation: "boom",
			TestID: "t", File: "f.py", Line: i + 1,
		})
	}
	got := report.RenderSummary(r, nil)
	if !strings.Contains(got, "and 5 more") {
		t.Errorf("summary lacks remainder note:\n%s", got)
	}
}

func TestMergeCI_SumsSummaries(t *testing.T) {
	merged := loadFixture(t, "mixed.json", "clean.json")
	if merged.Summary.Replay.Replayed != 7 || merged.Summary.Replay.Live != 1 {
		t.Errorf("replay = %+v, want replayed 7 live 1", merged.Summary.Replay)
	}
	if merged.Summary.Usage.ModelCalls != 3 {
		t.Errorf("usage model calls = %d, want 3", merged.Summary.Usage.ModelCalls)
	}
	if len(merged.Tests) != 6 {
		t.Errorf("tests = %d, want 6", len(merged.Tests))
	}
}

func TestLoadCI_WrongVersion_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(path, []byte(`{"report_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := report.LoadCI(path); err == nil || !strings.Contains(err.Error(), "expected 2") {
		t.Errorf("err = %v, want version error", err)
	}
}

func TestFindingRows_Mixed_ListsFailuresAndMissesWithLocations(t *testing.T) {
	rows := report.FindingRows(loadFixture(t, "mixed.json"))
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "rows_mixed.json", string(got)+"\n")
}

func TestGenerateJUnitXML_Mixed_MatchesGolden(t *testing.T) {
	out, err := report.GenerateJUnitXML(loadFixture(t, "mixed.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "junit_mixed.xml", string(out)+"\n")
}

func TestFindingRows_StrictAndAutoMissSharingATestID_BothListed(t *testing.T) {
	strict := &report.CIReport{ReportVersion: 2, Tests: []report.CITest{{
		TestID: "t.py::test_a", File: "t.py", Line: 3, Outcome: "failed",
		Replay: &report.CITestReplay{Fixtures: []string{"inc-9"}, Mode: "strict", Misses: 1, Miss: "replay miss: x"},
	}}}
	auto := &report.CIReport{ReportVersion: 2, Results: []report.CIResult{{
		AssertionID: "replay_inc-9", Status: "not_evaluated", Reason: "replay_miss",
		TestID: "t.py::test_a", File: "t.py", Line: 3,
	}}}

	rows := report.FindingRows(report.MergeCI([]*report.CIReport{strict, auto}))

	statuses := map[string]bool{}
	for _, r := range rows {
		statuses[r.Status] = true
		if r.Type != "replay" {
			t.Errorf("type = %q, want replay for a replay_miss row without a type", r.Type)
		}
	}
	if len(rows) != 2 || !statuses["hard_fail"] || !statuses["not_evaluated"] {
		t.Fatalf("rows = %+v, want a hard_fail and a not_evaluated row", rows)
	}
}
