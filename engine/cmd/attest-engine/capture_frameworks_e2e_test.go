package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func frameworkFixture(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("../../../protocol-tests/trace-v2", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Each framework route's export captures to a verified fixture with the
// expected steps.
func TestCaptureFrom_FrameworkRoutes_Verified(t *testing.T) {
	bin := buildEngineBinary(t)
	cases := map[string]string{
		"pydantic-ai-v5":   "trace: 2 llm_call, 1 tool_call; trace check OK",
		"vercel-ai-modern": "trace: 2 llm_call, 1 tool_call; trace check OK",
		"mastra":           "trace: 2 llm_call, 1 tool_call; trace check OK",
		"openai-agents-v2": "trace: 1 agent_call, 2 llm_call, 1 tool_call; trace check OK",
		"claude-code-py":   "trace: 2 llm_call, 1 tool_call; trace check OK",
		"claude-code-ts":   "trace: 2 llm_call, 1 tool_call; trace check OK",
		"langsmith-py":     "trace: 2 llm_call, 1 tool_call; trace check OK",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capture", "--name", name, "--from", frameworkFixture(t, "otlp/"+name+".otlp.json"), "--out-dir", dir)
			if code != 0 || !strings.Contains(stdout, want) || !strings.Contains(stdout, "verified: every drafted contract passes") {
				t.Fatalf("exit %d, want %q\n%s%s", code, want, stdout, stderr)
			}
		})
	}
}

func TestCaptureFrom_LangSmithToolArguments_DraftsCardinalityContract(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capture", "--name", "ls", "--from", frameworkFixture(t, "otlp/langsmith-py.otlp.json"), "--out-dir", t.TempDir())

	if code != 0 || !strings.Contains(stdout, "cardinality-lookup_order-per-order_id") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
}

// Exports with no mapped model or tool span are not verified, with or without
// --expect-fail, and the message names the cause and the fix.
func TestCaptureFrom_VacuousRoutes_NotVerified(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, name := range []string{"vercel-ai-legacy", "langgraph-py-openinference", "langgraph-ts-openinference"} {
		for _, extra := range [][]string{nil, {"--expect-fail"}} {
			t.Run(name+strings.Join(extra, ""), func(t *testing.T) {
				dir := t.TempDir()
				args := append([]string{"capture", "--name", name, "--from", frameworkFixture(t, "otlp-vacuous/"+name+".otlp.json"), "--out-dir", dir, "--json"}, extra...)

				stdout, stderr, code := runEngine(t, bin, t.TempDir(), args...)

				if code != 1 {
					t.Fatalf("exit %d, want 1\n%s%s", code, stdout, stderr)
				}
				var res struct {
					Verified bool `json:"verified"`
					Steps    int  `json:"steps"`
				}
				if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.Verified {
					t.Fatalf("result %q: verified %v err %v", stdout, res.Verified, err)
				}
				for _, want := range []string{"not verified: the trace has no llm_call or tool_call step", "none follow the GenAI semantic conventions attest maps", "attest-engine guide capture"} {
					if !strings.Contains(stderr, want) {
						t.Errorf("stderr lacks %q:\n%s", want, stderr)
					}
				}
				data, err := os.ReadFile(filepath.Join(dir, name+".attest.json"))
				if err != nil || !strings.Contains(string(data), `"verified": false`) {
					t.Errorf("fixture not written as unverified: %v\n%s", err, data)
				}
			})
		}
	}
}

func TestTraceCheck_NoGenAISpans_PointsToTheCaptureGuide(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "trace", "check", frameworkFixture(t, "otlp-vacuous/vercel-ai-legacy.otlp.json"))

	if code != 0 || !strings.Contains(stdout, "no_genai_spans") || !strings.Contains(stdout, "attest-engine guide capture") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
}
