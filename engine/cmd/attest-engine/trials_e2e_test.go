package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trialsReport writes a report-v2 with one judge assertion run as `trials`
// fixed-N trials, of which `passes` passed and the rest hard-failed.
func trialsReport(t *testing.T, dir, name string, passes, trials int) string {
	t.Helper()
	rows := make([]map[string]any, 0, trials)
	for i := 1; i <= trials; i++ {
		row := map[string]any{
			"assertion_id": "tone", "status": "pass", "score": 0.9, "explanation": "",
			"cost": 0.0, "duration_ms": 1, "layer": 6, "type": "llm_judge", "trial": i,
		}
		if i > passes {
			row["status"], row["score"] = "hard_fail", 0.2
		}
		rows = append(rows, row)
	}
	doc := map[string]any{
		"report_version":    2,
		"id_scheme":         2,
		"timestamp":         "2026-11-02T00:00:00Z",
		"summary":           map[string]int{"total": trials, "passed": passes, "soft_fail": 0, "hard_fail": trials - passes, "not_evaluated": 0},
		"results":           rows,
		"tests":             []any{},
		"total_cost":        0.0,
		"total_duration_ms": trials,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicy_RegressionRule_TrialVerdictsMapToExitCodes(t *testing.T) {
	// Arrange: baseline pinned from 20 trials with 18 passes (--flaky 0.1).
	bin := buildEngineBinary(t)
	cacheDir, dir := t.TempDir(), t.TempDir()
	base := trialsReport(t, dir, "base.json", 18, 20)
	if _, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", "flaky-0.1", "--report", base); code != 0 {
		t.Fatalf("baseline pin exit %d: %s", code, stderr)
	}
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("regression: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		passes   int
		verdict  string
		exitCode int
	}{
		{"inconclusive at 16/20", 16, "inconclusive", 2},
		{"regressed at 8/20", 8, "regressed", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cur := trialsReport(t, dir, fmt.Sprintf("cur-%d.json", c.passes), c.passes, 20)

			// Act: run twice to show the verdict is reproducible.
			var outputs []string
			for range 2 {
				stdout, stderr, code := runEngine(t, bin, cacheDir,
					"policy", "evaluate", "--policy", policyPath, "--report", cur, "--baseline", "flaky-0.1")
				if code != c.exitCode {
					t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, c.exitCode, stdout, stderr)
				}
				outputs = append(outputs, stdout)
			}

			// Assert
			if outputs[0] != outputs[1] {
				t.Errorf("verdict output differs across reruns:\n%s\n%s", outputs[0], outputs[1])
			}
			var out struct {
				Verdict string `json:"verdict"`
				Trials  []struct {
					Passes, Evaluated int
					Lower             float64 `json:"wilson_lower"`
					Upper             float64 `json:"wilson_upper"`
				} `json:"trials"`
			}
			if err := json.Unmarshal([]byte(outputs[0]), &out); err != nil {
				t.Fatalf("decode policy output: %v\n%s", err, outputs[0])
			}
			if out.Verdict != c.verdict || len(out.Trials) != 1 || out.Trials[0].Passes != c.passes {
				t.Errorf("output = %+v, want verdict %s over %d passes", out, c.verdict, c.passes)
			}
			t.Logf("%s: %s", c.name, strings.TrimSpace(outputs[0]))
		})
	}
}

func TestBaseline_PinTrialsReport_ShowsPassCounts(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir, dir := t.TempDir(), t.TempDir()
	report := trialsReport(t, dir, "base.json", 7, 10)

	if _, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", "t", "--report", report); code != 0 {
		t.Fatalf("baseline pin exit %d: %s", code, stderr)
	}
	stdout, stderr, code := runEngine(t, bin, cacheDir, "baseline", "show", "--tag", "t")

	if code != 0 {
		t.Fatalf("baseline show exit %d: %s", code, stderr)
	}
	var out struct {
		Entries []struct {
			AssertionID string `json:"assertion_id"`
			Passes      int    `json:"passes"`
			Evaluated   int    `json:"evaluated"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if len(out.Entries) != 1 || out.Entries[0].Passes != 7 || out.Entries[0].Evaluated != 10 {
		t.Errorf("entries = %+v, want one entry with 7/10", out.Entries)
	}
}
