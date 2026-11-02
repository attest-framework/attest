package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

const inc1042OTLP = "../../../protocol-tests/trace-v2/otlp/inc-1042-refund.otlp.json"

// runTraceEngine runs the engine binary and returns stdout, stderr and the exit
// code.
func runTraceEngine(t *testing.T, bin string, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0
	case errors.As(err, &exitErr):
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("run %v: %v", args, err)
	return "", "", -1
}

func TestTraceCheck_INC1042_StructureOKWithCoverageNotes(t *testing.T) {
	bin := buildEngineBinary(t)
	out, stderr, code := runTraceEngine(t, bin, nil, "trace", "check", inc1042OTLP)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
	for _, want := range []string{
		"structure: OK",
		"coverage: missing evidence",
		"cost     5 of 5 llm_call spans lack gen_ai.usage.cost",
		"content  5 of 5 llm_call spans lack gen_ai.input.messages / gen_ai.output.messages",
		"attest trace schema v2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTraceCheck_CorruptedCopy_ExitsOneListingSpanIDs(t *testing.T) {
	bin := buildEngineBinary(t)
	data, err := os.ReadFile(inc1042OTLP)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	scope := doc["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)
	var kept []any
	var orphanID, unpairedID string
	spans := scope["spans"].([]any)
	for i, raw := range spans {
		s := raw.(map[string]any)
		switch s["name"] {
		case "execute_tool lookup_order":
			unpairedID = spans[i-1].(map[string]any)["spanId"].(string)
			continue
		case "execute_tool notify_customer":
			s["parentSpanId"] = "deadbeefdeadbeef"
			orphanID = s["spanId"].(string)
		}
		kept = append(kept, s)
	}
	scope["spans"] = kept
	corrupted, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "corrupted.otlp.json")
	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, code := runTraceEngine(t, bin, nil, "trace", "check", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	for _, want := range []string{"orphan_span", "span " + orphanID, "unpaired_tool_call", "span " + unpairedID} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	jsonOut, _, code := runTraceEngine(t, bin, nil, "trace", "check", "--json", path)
	if code != 1 {
		t.Fatalf("--json exit %d, want 1", code)
	}
	var report trace.CheckReport
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("--json output is not a check report: %v\n%s", err, jsonOut)
	}
	if report.OK || len(report.Errors()) < 2 {
		t.Errorf("report ok=%v errors=%+v", report.OK, report.Errors())
	}
}

func TestTraceCheck_JSONFromStdin(t *testing.T) {
	bin := buildEngineBinary(t)
	data, err := os.ReadFile(inc1042OTLP)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runTraceEngine(t, bin, data, "trace", "check", "--json", "-")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	var report trace.CheckReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.SchemaVersion != 2 || report.Spans != 10 || len(report.Coverage) != 2 {
		t.Errorf("report = %+v", report)
	}
}

func TestTraceCommand_MalformedInputAndUsage(t *testing.T) {
	bin := buildEngineBinary(t)
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"zz"}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"trace", "check", bad}, 1, "traceId"},
		{[]string{"trace", "convert", bad}, 1, "traceId"},
		{[]string{"trace", "check", filepath.Join(t.TempDir(), "missing.json")}, 1, "no such file"},
		{[]string{"trace"}, 2, "usage"},
		{[]string{"trace", "check"}, 2, "usage"},
		{[]string{"trace", "lint", bad}, 2, "unknown trace subcommand"},
	}
	for _, tc := range cases {
		_, stderr, code := runTraceEngine(t, bin, nil, tc.args...)
		if code != tc.code || !strings.Contains(stderr, tc.msg) {
			t.Errorf("%v: exit %d stderr %q, want exit %d containing %q", tc.args, code, stderr, tc.code, tc.msg)
		}
	}
}

// Acceptance: cost_under on a converted trace without cost evidence is
// not_evaluated(missing_evidence) naming metadata.cost_usd.
func TestTraceConvert_INC1042_CostUnderIsMissingEvidence(t *testing.T) {
	bin := buildEngineBinary(t)
	out, stderr, code := runTraceEngine(t, bin, nil, "trace", "convert", inc1042OTLP)
	if code != 0 {
		t.Fatalf("convert exit %d: %s", code, stderr)
	}
	var tr types.Trace
	if err := json.Unmarshal([]byte(out), &tr); err != nil {
		t.Fatalf("convert output is not a trace: %v", err)
	}
	if rpcErr := trace.Validate(&tr, len(out)); rpcErr != nil {
		t.Fatalf("converted trace invalid: %s", rpcErr.Message)
	}

	p := assertion.NewPipeline(assertion.NewRegistry())
	batch, err := p.EvaluateBatch(context.Background(), &tr, []types.Assertion{{
		AssertionID: "cost-under",
		Type:        types.TypeConstraint,
		Spec:        json.RawMessage(`{"field":"metadata.cost_usd","operator":"lte","value":0.05}`),
	}})
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	r := batch.Results[0]
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonMissingEvidence || r.Explanation != "metadata.cost_usd absent" {
		t.Errorf("result = %s/%s %q, want not_evaluated/missing_evidence \"metadata.cost_usd absent\"", r.Status, r.Reason, r.Explanation)
	}
}
