package report_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
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

func TestFindingRows_PassedTestWithFailedTrial_HasNoRow(t *testing.T) {
	r := &report.CIReport{ReportVersion: 2,
		Results: []report.CIResult{{AssertionID: "a1", Status: "hard_fail", Type: "content", TestID: "t.py::test_x", File: "t.py", Line: 3}},
		Tests:   []report.CITest{{TestID: "t.py::test_x", File: "t.py", Line: 3, Outcome: "passed"}},
	}
	if rows := report.FindingRows(r); len(rows) != 0 {
		t.Errorf("rows = %+v, want none for a passed test", rows)
	}
}

func TestFindingRows_StrictMissInAutoLabelledTest_IsListed(t *testing.T) {
	r := &report.CIReport{ReportVersion: 2, Tests: []report.CITest{{
		TestID: "t.py::test_a", File: "t.py", Line: 3, Outcome: "failed",
		Replay: &report.CITestReplay{Fixtures: []string{"inc-9"}, Mode: "auto", Misses: 1, Miss: "replay miss: x"},
	}}}
	if rows := report.FindingRows(r); len(rows) != 1 || rows[0].Status != "hard_fail" {
		t.Errorf("rows = %+v, want one hard_fail replay_miss row", rows)
	}
}

func TestFindingRows_FailedTestWithAutoMissRow_IsNotDuplicated(t *testing.T) {
	r := &report.CIReport{ReportVersion: 2,
		Results: []report.CIResult{{AssertionID: "replay_inc-9", Status: "not_evaluated", Reason: "replay_miss", TestID: "t.py::test_a", File: "t.py", Line: 3}},
		Tests: []report.CITest{{TestID: "t.py::test_a", File: "t.py", Line: 3, Outcome: "failed",
			Replay: &report.CITestReplay{Fixtures: []string{"inc-9"}, Mode: "auto", Misses: 1, Miss: "replay miss: x"}}},
	}
	if rows := report.FindingRows(r); len(rows) != 1 {
		t.Errorf("rows = %+v, want one", rows)
	}
}

func TestRenderSummary_XfailPlainSkipAndNotEvaluatedSkip_CountsEachOnce(t *testing.T) {
	got := report.RenderSummary(loadFixture(t, "skips.json"), nil)
	golden(t, "summary_skips.md", got)
	if first := strings.SplitN(got, "\n", 2)[0]; first != "### attest: 1 passed · 0 failed · 1 not evaluated · 2 skipped" {
		t.Errorf("header = %q", first)
	}
}

func TestRenderSummary_NoSkippedTests_HeaderHasNoSkippedCount(t *testing.T) {
	got := report.RenderSummary(loadFixture(t, "clean.json"), nil)
	if first := strings.SplitN(got, "\n", 2)[0]; first != "### attest: 1 passed · 0 failed · 0 not evaluated" {
		t.Errorf("header = %q", first)
	}
}

func TestFindingRows_ExpectedFailureInSkippedTest_HasNoRow(t *testing.T) {
	if rows := report.FindingRows(loadFixture(t, "skips.json")); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: a skipped test's hard_fail is an expected failure", rows)
	}
}

func TestFindingRows_SkippedTestWithOtherRowsAndAReplayMissRow_IsDropped(t *testing.T) {
	r := &report.CIReport{ReportVersion: 2,
		Results: []report.CIResult{
			{AssertionID: "a", Status: "pass", TestID: "t::x", File: "t.py", Line: 1},
			{AssertionID: "replay_f", Status: "not_evaluated", Reason: "replay_miss", TestID: "t::x", File: "t.py", Line: 1},
		},
		Tests: []report.CITest{{TestID: "t::x", File: "t.py", Line: 1, Outcome: "skipped"}},
	}
	if rows := report.FindingRows(r); len(rows) != 0 {
		t.Errorf("rows = %+v, want none for a skipped test with mixed rows", rows)
	}
}

func TestGenerateJUnitXML_Skips_MatchesGolden(t *testing.T) {
	out, err := report.GenerateJUnitXML(loadFixture(t, "skips.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "junit_skips.xml", string(out)+"\n")
	for _, want := range []string{`<skipped message="skipped">`, `<skipped message="not evaluated: provider_not_configured">`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("junit lacks %s", want)
		}
	}
	if strings.Contains(string(out), "<failure") {
		t.Error("an expected failure must not become a <failure>")
	}
}

const hostilePolicyDetail = "<!-- attest:pr-annotate --> @octocat [x](https://evil) <img src=x> unclosed <!--"

func TestRenderSummary_HostileReport_IsInert(t *testing.T) {
	policy := &report.PolicyOutcome{
		ExitCode: 1,
		Verdict:  "@org/team",
		Violations: []report.PolicyViolation{
			{Rule: "rule`with`ticks", Detail: hostilePolicyDetail, Severity: "block@x"},
		},
	}
	got := report.RenderSummary(loadFixture(t, "hostile.json"), policy)
	golden(t, "summary_hostile.md", got)

	outside := stripCodeSpans(got)
	for _, trigger := range []string{
		"@octocat", "@org", "org/repo#1", "<img", "<!--", "-->", "](", "evil", "attest:pr-annotate",
		"‮", "​",
	} {
		if strings.Contains(outside, trigger) {
			t.Errorf("%q appears outside a code span:\n%s", trigger, outside)
		}
	}
	if !strings.Contains(got, "| `tests/t.py:5` | `tests/t.py::test_pipe` | `effect` | `a\\\\|b` |") {
		t.Errorf("a backslash before a pipe must render as a backslash and an escaped pipe in one cell:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "| `") && strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") != 5 {
			t.Errorf("table row has extra unescaped pipes: %s", line)
		}
	}
}

var spanRun = regexp.MustCompile("`+")

// stripCodeSpans removes inline code spans (CommonMark: an opening backtick
// run closes at the next run of the same length) from s.
func stripCodeSpans(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		loc := spanRun.FindStringIndex(s)
		if loc == nil {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:loc[0]])
		fence := s[loc[0]:loc[1]]
		rest := s[loc[1]:]
		end := -1
		for off := 0; off < len(rest); {
			l := spanRun.FindStringIndex(rest[off:])
			if l == nil {
				break
			}
			if rest[off+l[0]:off+l[1]] == fence {
				end = off + l[1]
				break
			}
			off += l[1]
		}
		if end < 0 {
			out.WriteString(fence)
			s = rest
			continue
		}
		s = rest[end:]
	}
	return out.String()
}

func TestCodeSpan_BacktickRuns_GetALongerFence(t *testing.T) {
	cases := map[string]string{
		"plain":       "`plain`",
		"a`b":         "``a`b``",
		"a``b":        "```a``b```",
		"`start":      "`` `start ``",
		"end`":        "`` end` ``",
		"a|b":         `a\|b`,
		"line\nbreak": "`line break`",
		"z​w‮x":       "`zwx`",
	}
	for in, want := range cases {
		got := report.CodeSpanForTest(in, in == "a|b")
		if in == "a|b" {
			want = "`" + want + "`"
		}
		if got != want {
			t.Errorf("codeSpan(%q) = %q, want %q", in, got, want)
		}
	}
}
