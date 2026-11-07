package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// reportWithStatuses is a report-v2 body with assertions a and b at the given statuses.
func reportWithStatuses(a, b string) string {
	return `{"report_version":2,"id_scheme":2,"results":[` +
		`{"assertion_id":"a","status":"` + a + `","score":1,"layer":1,"type":"schema"},` +
		`{"assertion_id":"b","status":"` + b + `","score":1,"layer":1,"type":"schema"}],"tests":[]}`
}

// pinBaseline pins a report where both assertions pass as tag in cacheDir.
func pinBaseline(t *testing.T, bin, cacheDir, tag string) {
	t.Helper()
	clean := filepath.Join(t.TempDir(), "clean.json")
	cliWriteFile(t, clean, reportWithStatuses("pass", "pass"))
	stdout, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", tag, "--report", clean)
	if code != 0 {
		t.Fatalf("pin exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

func TestPolicyEvaluate_RegressionRuleWithoutBaseline_ExitsUsageNamingBaseline(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()
	pinBaseline(t, bin, cacheDir, "main")
	regressed := filepath.Join(t.TempDir(), "regressed.json")
	cliWriteFile(t, regressed, reportWithStatuses("pass", "hard_fail"))

	cases := []struct {
		name   string
		policy string
	}{
		{"block_on_regression", "block_on_regression: true\n"},
		{"regression trials rule", "regression: {}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := filepath.Join(t.TempDir(), "attest.policy.yaml")
			cliWriteFile(t, policyPath, tc.policy)

			stdout, stderr, code := runEngine(t, bin, cacheDir, "policy", "evaluate", "--policy", policyPath, "--report", regressed)
			if code != 3 {
				t.Fatalf("without --baseline: exit=%d, want 3\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, "--baseline") {
				t.Errorf("stderr must name the missing --baseline input, got: %s", stderr)
			}

			_, stderr, code = runEngine(t, bin, cacheDir, "policy", "evaluate", "--policy", policyPath, "--report", regressed, "--baseline", "main")
			if tc.name == "block_on_regression" && code != 1 {
				t.Errorf("with --baseline: exit=%d, want 1 (regression blocks)\nstderr: %s", code, stderr)
			}
			if tc.name != "block_on_regression" && code == 3 {
				t.Errorf("with --baseline: exit=3, want a verdict\nstderr: %s", stderr)
			}
		})
	}
}

func TestPolicyEvaluate_MutationRuleWithoutMutation_ExitsUsageNamingMutation(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	report := cliReportV2(t, dir)
	policyPath := filepath.Join(dir, "attest.policy.yaml")
	cliWriteFile(t, policyPath, "min_mutation_score:\n  score: 0.8\n")

	_, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", report)
	if code != 3 || !strings.Contains(stderr, "--mutation") {
		t.Fatalf("exit=%d, want 3 naming --mutation\nstderr: %s", code, stderr)
	}
}

func TestPolicyEvaluate_ReportWithNoTestsButCollected_ExitsUsage(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "attest.policy.yaml")
	cliWriteFile(t, policyPath, "layers:\n  1:\n    max_hard_fails: 0\n")

	cases := []struct {
		name     string
		body     string
		wantExit int
	}{
		{"collected 4, tests empty", `{"report_version":2,"results":[],"tests":[],"tests_collected":4}`, 3},
		{"collected 4, tests absent", `{"report_version":2,"results":[],"tests_collected":4}`, 3},
		{"collected absent, tests empty", `{"report_version":2,"results":[],"tests":[]}`, 0},
		{"collected 0, tests empty", `{"report_version":2,"results":[],"tests":[],"tests_collected":0}`, 0},
		{"collected 1, one test", `{"report_version":2,"results":[],"tests":[{"test_id":"t","name":"t","file":"f","line":1,"outcome":"passed"}],"tests_collected":1}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "report.json")
			cliWriteFile(t, reportPath, tc.body)
			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath)
			if code != tc.wantExit {
				t.Fatalf("exit=%d, want %d\nstdout: %s\nstderr: %s", code, tc.wantExit, stdout, stderr)
			}
			if tc.wantExit == 3 && !strings.Contains(stderr, "tests_collected") {
				t.Errorf("stderr must name tests_collected, got: %s", stderr)
			}
		})
	}
}
