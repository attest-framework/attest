package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildEngineBinary compiles the attest-engine binary into the test's
// TempDir and returns the path. Tests can then exec it directly so the
// CLI surface is exercised end-to-end (cobra-less flag parsing, subcommand
// dispatch, exit codes).
func buildEngineBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "attest-engine")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build engine binary: %v", err)
	}
	return bin
}

// runEngine executes bin with args using cacheDir as ATTEST_CACHE_DIR so
// each test has its own SQLite DB and tests can run in parallel. Returns
// stdout, stderr, and exit code (0 on success).
func runEngine(t *testing.T, bin, cacheDir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "ATTEST_CACHE_DIR="+cacheDir)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exitErr.ExitCode()
		}
		t.Fatalf("run engine: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String(), stderr.String(), 0
}

// minimalReportV2 returns a JSON v2 report containing `passed` passing
// schema assertions and `failed` failing schema assertions. Used by the
// pin/policy flow tests.
func minimalReportV2(passed, failed int) string {
	type result struct {
		AssertionID  string  `json:"assertion_id"`
		Status       string  `json:"status"`
		Score        float64 `json:"score"`
		Explanation  string  `json:"explanation"`
		Cost         float64 `json:"cost"`
		DurationMS   int64   `json:"duration_ms"`
		Layer        int     `json:"layer"`
		Type         string  `json:"type"`
		FailureClass string  `json:"failure_class,omitempty"`
	}
	type envelope struct {
		ReportVersion   int      `json:"report_version"`
		Timestamp       string   `json:"timestamp"`
		Results         []result `json:"results"`
		TotalCost       float64  `json:"total_cost"`
		TotalDurationMS int64    `json:"total_duration_ms"`
	}
	env := envelope{
		ReportVersion: 2,
		Timestamp:     "2026-05-01T00:00:00Z",
	}
	for i := 0; i < passed; i++ {
		env.Results = append(env.Results, result{
			AssertionID: "pass-" + string(rune('a'+i)),
			Status:      "pass",
			Score:       1.0,
			Layer:       1,
			Type:        "schema",
			DurationMS:  5,
		})
	}
	for i := 0; i < failed; i++ {
		env.Results = append(env.Results, result{
			AssertionID:  "fail-" + string(rune('a'+i)),
			Status:       "hard_fail",
			Score:        0.0,
			Layer:        1,
			Type:         "schema",
			FailureClass: "broken_code",
			DurationMS:   3,
		})
	}
	b, _ := json.MarshalIndent(env, "", "  ")
	return string(b)
}

func TestBaseline_PinListShow_RoundTrip(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(minimalReportV2(2, 1)), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}

	// Pin succeeds.
	stdout, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", "v1.0.0", "--report", reportPath)
	if code != 0 {
		t.Fatalf("pin exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"assertion_count": 3`) {
		t.Errorf("pin stdout missing assertion_count=3:\n%s", stdout)
	}

	// List shows it.
	stdout, _, code = runEngine(t, bin, cacheDir, "baseline", "list")
	if code != 0 {
		t.Fatalf("list exited %d", code)
	}
	if !strings.Contains(stdout, "v1.0.0") {
		t.Errorf("list missing pinned tag:\n%s", stdout)
	}

	// Show returns the entries.
	stdout, _, code = runEngine(t, bin, cacheDir, "baseline", "show", "--tag", "v1.0.0")
	if code != 0 {
		t.Fatalf("show exited %d", code)
	}
	if !strings.Contains(stdout, "pass-a") || !strings.Contains(stdout, "fail-a") {
		t.Errorf("show stdout missing entries:\n%s", stdout)
	}

	// Delete removes it.
	stdout, _, code = runEngine(t, bin, cacheDir, "baseline", "delete", "--tag", "v1.0.0")
	if code != 0 {
		t.Fatalf("delete exited %d", code)
	}
	if !strings.Contains(stdout, `"deleted": 3`) {
		t.Errorf("delete stdout wrong:\n%s", stdout)
	}
}

func TestPolicy_EvaluateExitCodes(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()
	reportDir := t.TempDir()

	// 3 passing, 0 failing — policy that disallows hard fails on layer 1
	// must pass.
	cleanReport := filepath.Join(reportDir, "clean.json")
	if err := os.WriteFile(cleanReport, []byte(minimalReportV2(3, 0)), 0o600); err != nil {
		t.Fatalf("write clean report: %v", err)
	}

	// 1 passing, 2 failing — same policy must block.
	dirtyReport := filepath.Join(reportDir, "dirty.json")
	if err := os.WriteFile(dirtyReport, []byte(minimalReportV2(1, 2)), 0o600); err != nil {
		t.Fatalf("write dirty report: %v", err)
	}

	policyPath := filepath.Join(reportDir, "policy.yaml")
	policyYAML := `layers:
  1:
    max_hard_fails: 0
`
	if err := os.WriteFile(policyPath, []byte(policyYAML), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	_, _, code := runEngine(t, bin, cacheDir,
		"policy", "evaluate", "--policy", policyPath, "--report", cleanReport)
	if code != 0 {
		t.Errorf("clean report should exit 0, got %d", code)
	}

	_, stderr, code := runEngine(t, bin, cacheDir,
		"policy", "evaluate", "--policy", policyPath, "--report", dirtyReport)
	if code != 1 {
		t.Errorf("dirty report should exit 1, got %d (stderr: %s)", code, stderr)
	}
}

func TestPolicy_FailingLoadExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()

	_, _, code := runEngine(t, bin, cacheDir, "policy", "evaluate")
	if code != 2 {
		t.Errorf("missing flags should exit 2, got %d", code)
	}
}
