package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/internal/assertion/judge"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// outageReport runs a judge assertion against a failing mock provider and
// writes the resulting v2 report, returning its path.
func outageReport(t *testing.T, dir string) string {
	t.Helper()
	provider := llm.NewMockProvider(nil, []error{errors.New("503 service unavailable")})
	p := assertion.NewPipeline(assertion.NewRegistry(assertion.WithJudge(provider, judge.NewRubricRegistry(), nil)))
	p.SetSimulation(false)
	trace := &types.Trace{TraceID: "trc_outage", Output: json.RawMessage(`{"message":"refund processed"}`)}
	batch, err := p.EvaluateBatch(context.Background(), trace, []types.Assertion{{
		AssertionID: "judge-outage",
		Type:        types.TypeLLMJudge,
		Spec:        json.RawMessage(`{"target":"output.message","threshold":0.5}`),
	}})
	if err != nil {
		t.Fatalf("EvaluateBatch: %v", err)
	}
	r := batch.Results[0]
	if r.Status != types.StatusNotEvaluated || r.Reason != types.ReasonProviderUnavailable {
		t.Fatalf("status=%q reason=%q, want not_evaluated/provider_unavailable", r.Status, r.Reason)
	}

	data, err := json.Marshal(map[string]any{
		"report_version": 2,
		"id_scheme":      2,
		"results":        batch.Results,
		"total_cost":     batch.TotalCost,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	return path
}

func TestPolicyEvaluate_ProviderOutage_WarnsByDefault(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	reportPath := outageReport(t, dir)

	cases := []struct {
		name     string
		policy   string
		wantExit int
	}{
		{"default policy warns", "layers:\n  0:\n    max_hard_fails: 0\n", 2},
		{"not_evaluated block", "not_evaluated: block\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := filepath.Join(t.TempDir(), "attest.policy.yaml")
			if err := os.WriteFile(policyPath, []byte(tc.policy), 0o600); err != nil {
				t.Fatalf("write policy: %v", err)
			}

			stdout, stderr, code := runEngine(t, bin, dir, "policy", "evaluate", "--policy", policyPath, "--report", reportPath)

			if code != tc.wantExit {
				t.Fatalf("exit=%d, want %d\nstdout: %s\nstderr: %s", code, tc.wantExit, stdout, stderr)
			}
		})
	}
}
