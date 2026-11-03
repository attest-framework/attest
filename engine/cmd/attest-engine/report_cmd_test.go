package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mixedReport = "../../internal/report/testdata/ci/mixed.json"

func TestReportSummary_TwoReports_MergesAndPrintsHeaderFirst(t *testing.T) {
	var out, errOut bytes.Buffer
	code := reportSummary([]string{"--report", mixedReport, "--report", "../../internal/report/testdata/ci/clean.json"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	first := strings.SplitN(out.String(), "\n", 2)[0]
	if first != "### attest: 2 passed · 2 failed · 2 not evaluated" {
		t.Errorf("first line = %q", first)
	}
	if !strings.Contains(out.String(), "7 replayed · 1 live model calls · $0.04") {
		t.Errorf("merged replay line missing:\n%s", out.String())
	}
}

func TestReportSummary_PolicyResult_ShowsVerdict(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"violations":[{"rule":"hard_fail","detail":"1 hard failure","severity":"block"}],"exit_code":1,"passed":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := reportSummary([]string{"--report", mixedReport, "--policy-result", policyPath}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "**Policy:** fail (exit 1)") {
		t.Errorf("policy verdict missing:\n%s", out.String())
	}
}

func TestReportSummary_MissingReport_Exit2(t *testing.T) {
	var out, errOut bytes.Buffer
	code := reportSummary([]string{"--report", "does-not-exist.json"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "does-not-exist.json") || out.Len() != 0 {
		t.Errorf("code %d stderr %q stdout %q", code, errOut.String(), out.String())
	}
}

func TestReportSummary_NoReports_Exit2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := reportSummary(nil, &out, &errOut); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func TestReportJUnit_WritesFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "junit.xml")
	var errOut bytes.Buffer
	if code := reportJUnit([]string{"--report", mixedReport, "--out", outPath}, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(data), `<failure message="effect violated: 2 refunds for O-1042 (expected 1)"`) {
		t.Errorf("junit file: %v\n%s", err, data)
	}
}

func TestReportJUnit_MissingOut_Exit2(t *testing.T) {
	var errOut bytes.Buffer
	if code := reportJUnit([]string{"--report", mixedReport}, &errOut); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func TestReportRows_JSON_IsOneArrayOfRows(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := reportRows([]string{"--report", mixedReport, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not one JSON array: %v\n%s", err, out.String())
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for _, key := range []string{"file", "line", "test_id", "assertion_id", "type", "status", "reason", "explanation"} {
		if _, ok := rows[0][key]; !ok {
			t.Errorf("row lacks %q: %v", key, rows[0])
		}
	}
}

func TestReportRows_NoFindings_PrintsEmptyArray(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := reportRows([]string{"--report", "../../internal/report/testdata/ci/clean.json", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("stdout = %q, want []", out.String())
	}
}
