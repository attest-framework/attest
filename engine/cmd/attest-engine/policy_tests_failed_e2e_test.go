package main_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// nativeFailureReport reproduces a passing Attest assertion followed by a
// failed native assertion: only the test outcome records the failure.
func nativeFailureReport(outcome string) string {
	return `{"report_version":2,"id_scheme":2,"tests_collected":1,"results":[` +
		`{"assertion_id":"a","status":"pass","score":1,"layer":1,"type":"schema"}],` +
		`"tests":[{"test_id":"tests/test_agent.py::test_business_limit","name":"test_business_limit",` +
		`"file":"tests/test_agent.py","line":8,"outcome":"` + outcome + `"}]}`
}

func TestPolicyEvaluate_TestsFailed(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.json")
	cliWriteFile(t, baseline, nativeFailureReport("passed"))
	if stdout, stderr, code := runEngine(t, bin, dir, "baseline", "pin", "--tag", "main", "--report", baseline); code != 0 {
		t.Fatalf("pin exit=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	for _, tc := range []struct {
		name, outcome, policy string
		wantExit              int
	}{
		{"native failure blocks", "failed", "{}\n", 1},
		{"global warn does not downgrade", "failed", "severity: warn\n", 1},
		{"explicit block", "failed", "tests_failed: block\n", 1},
		{"warn", "failed", "tests_failed: warn\n", 2},
		{"off", "failed", "tests_failed: off\n", 0},
		{"passed", "passed", "{}\n", 0},
		{"skipped including normalized xfail", "skipped", "{}\n", 0},
		{"strict XPASS counts as failed", "failed", "{}\n", 1},
		{"unchanged baseline does not erase native failure", "failed", "block_on_regression: true\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := filepath.Join(t.TempDir(), "policy.yaml")
			reportPath := filepath.Join(t.TempDir(), "report.json")
			cliWriteFile(t, policyPath, tc.policy)
			cliWriteFile(t, reportPath, nativeFailureReport(tc.outcome))
			args := []string{"policy", "evaluate", "--policy", policyPath, "--report", reportPath}
			if strings.Contains(tc.policy, "block_on_regression") {
				args = append(args, "--baseline", "main")
			}
			stdout, stderr, code := runEngine(t, bin, dir, args...)
			if code != tc.wantExit {
				t.Fatalf("exit=%d, want %d\nstdout: %s\nstderr: %s", code, tc.wantExit, stdout, stderr)
			}
			var verdict struct {
				ExitCode   int  `json:"exit_code"`
				Passed     bool `json:"passed"`
				Violations []struct {
					Rule, Severity, Detail string
				} `json:"violations"`
			}
			if err := json.Unmarshal([]byte(stdout), &verdict); err != nil {
				t.Fatalf("decode verdict: %v\n%s", err, stdout)
			}
			if verdict.ExitCode != tc.wantExit || verdict.Passed != (tc.wantExit == 0) {
				t.Fatalf("inconsistent verdict: %s", stdout)
			}
			if tc.wantExit == 0 {
				if len(verdict.Violations) != 0 {
					t.Fatalf("unexpected violations: %s", stdout)
				}
				return
			}
			if len(verdict.Violations) != 1 || verdict.Violations[0].Rule != "tests_failed" {
				t.Fatalf("want tests_failed violation, got: %s", stdout)
			}
			wantSeverity := "block"
			if tc.wantExit == 2 {
				wantSeverity = "warn"
			}
			if verdict.Violations[0].Severity != wantSeverity || !strings.Contains(verdict.Violations[0].Detail, "1") {
				t.Fatalf("want failed test count and severity %s, got: %s", wantSeverity, stdout)
			}
		})
	}
}

func TestPolicyEvaluate_TestsFailedOffKeepsBaselineRules(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	pinBaseline(t, bin, dir, "main")
	for _, tc := range []struct {
		name, report, rules, wantRule string
	}{
		{"regression", reportWithStatuses("pass", "hard_fail"), "block_on_regression: true\n", "block_on_regression"},
		{"removal", `{"report_version":2,"id_scheme":2,"results":[{"assertion_id":"a","status":"pass","score":1,"layer":1,"type":"schema"}],"tests":[]}`, "", "block_on_removal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := filepath.Join(t.TempDir(), "policy.yaml")
			reportPath := filepath.Join(t.TempDir(), "report.json")
			cliWriteFile(t, policyPath, "tests_failed: off\n"+tc.rules)
			cliWriteFile(t, reportPath, tc.report)
			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath, "--baseline", "main")
			if code != 1 || !strings.Contains(stdout, tc.wantRule) || strings.Contains(stdout, "tests_failed") {
				t.Fatalf("want independent %s block, exit=%d\nstdout: %s\nstderr: %s", tc.wantRule, code, stdout, stderr)
			}
		})
	}
}

func TestPolicyEvaluate_TestsFailedOffKeepsInputErrors(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, tc := range []struct {
		name, rules, body, missing string
	}{
		{"baseline", "block_on_regression: true\n", nativeFailureReport("failed"), "--baseline"},
		{"trials baseline", "regression: {}\n", nativeFailureReport("failed"), "--baseline"},
		{"mutation", "min_mutation_score:\n  score: 0.8\n", nativeFailureReport("failed"), "--mutation"},
		{"collected tests", "", `{"report_version":2,"results":[],"tests":[],"tests_collected":1}`, "tests_collected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			policyPath, reportPath := filepath.Join(dir, "policy.yaml"), filepath.Join(dir, "report.json")
			cliWriteFile(t, policyPath, "tests_failed: off\n"+tc.rules)
			cliWriteFile(t, reportPath, tc.body)
			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath)
			if code != 3 || !strings.Contains(stderr, tc.missing) {
				t.Fatalf("want usage error naming %s, exit=%d\nstdout: %s\nstderr: %s", tc.missing, code, stdout, stderr)
			}
		})
	}
}

func TestPolicyEvaluate_InvalidTestOutcomesAreUsageErrors(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, setting := range []string{"block", "warn", "off"} {
		for _, outcome := range []string{"Failed", "error", ""} {
			t.Run(setting+"/"+outcome, func(t *testing.T) {
				dir := t.TempDir()
				policyPath, reportPath := filepath.Join(dir, "policy.yaml"), filepath.Join(dir, "report.json")
				cliWriteFile(t, policyPath, "tests_failed: "+setting+"\n")
				cliWriteFile(t, reportPath, nativeFailureReport(outcome))
				stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath)
				if code != 3 || !strings.Contains(stderr, "tests/test_agent.py::test_business_limit") || !strings.Contains(stderr, `"`+outcome+`"`) {
					t.Fatalf("want usage error naming test and outcome %q, exit=%d\nstdout: %s\nstderr: %s", outcome, code, stdout, stderr)
				}
			})
		}
	}
}

func TestPolicyEvaluate_MissingTestsAreUsageErrors(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, body := range []string{
		`{"report_version":2,"results":[]}`,
		`{"report_version":2,"results":[],"tests":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			policyPath, reportPath := filepath.Join(dir, "policy.yaml"), filepath.Join(dir, "report.json")
			cliWriteFile(t, policyPath, "tests_failed: off\n")
			cliWriteFile(t, reportPath, body)
			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath)
			if code != 3 || !strings.Contains(stderr, "tests") {
				t.Fatalf("want usage error naming missing tests, exit=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
		})
	}
}

func TestPolicyEvaluate_ValidProtocolTestOutcomes(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	cliWriteFile(t, policyPath, "tests_failed: off\n")
	fixtures, err := filepath.Glob("../../../protocol-tests/report-v2/valid/*.json")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("find valid report-v2 fixtures: %v, %v", fixtures, err)
	}
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", fixture)
			if code == 3 {
				t.Fatalf("valid report-v2 fixture rejected\nstdout: %s\nstderr: %s", stdout, stderr)
			}
		})
	}
}
