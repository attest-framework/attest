package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-ai/attest/engine/internal/policy"
	"github.com/attest-ai/attest/engine/pkg/types"
)

const verdictFixtureDir = "../../../protocol-tests/verdicts"

type verdictFixture struct {
	Name     string                  `json:"name"`
	Results  []types.AssertionResult `json:"results"`
	Expected struct {
		PolicyExit int `json:"policyExit"`
	} `json:"expected"`
}

var validReasons = map[string]bool{
	types.ReasonProviderUnavailable:   true,
	types.ReasonProviderNotConfigured: true,
	types.ReasonSimulation:            true,
	types.ReasonMissingEvidence:       true,
	types.ReasonShortCircuited:        true,
}

func loadVerdictFixtures(t *testing.T) []verdictFixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(verdictFixtureDir, "[0-9]*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no verdict fixtures in %s (err=%v)", verdictFixtureDir, err)
	}
	out := make([]verdictFixture, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var f verdictFixture
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}
		out = append(out, f)
	}
	return out
}

// TestVerdictFixtures_WireFormatAndPolicyExit checks each fixture's rows
// against the C1 wire rules and its policy exit code under policy.yaml.
func TestVerdictFixtures_WireFormatAndPolicyExit(t *testing.T) {
	pol, err := policy.Load(filepath.Join(verdictFixtureDir, "policy.yaml"))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	seen := map[string]bool{}

	for _, f := range loadVerdictFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			for _, r := range f.Results {
				seen[r.Status] = true
				if r.Status == types.StatusNotEvaluated {
					seen[r.Reason] = true
					if !validReasons[r.Reason] {
						t.Errorf("%s: reason %q is not a C1 reason", r.AssertionID, r.Reason)
					}
					if r.FailureClass != "" {
						t.Errorf("%s: failure_class set on not_evaluated", r.AssertionID)
					}
				} else if r.Reason != "" {
					t.Errorf("%s: reason %q set on status %q", r.AssertionID, r.Reason, r.Status)
				}
			}

			res := policy.Evaluate(pol, f.Results, 0, nil)
			got := res.ExitCode()

			if got != f.Expected.PolicyExit {
				t.Errorf("policy exit = %d, want %d", got, f.Expected.PolicyExit)
			}
		})
	}

	for _, want := range []string{
		types.StatusPass, types.StatusSoftFail, types.StatusHardFail, types.StatusNotEvaluated,
		types.ReasonProviderUnavailable, types.ReasonProviderNotConfigured, types.ReasonSimulation,
		types.ReasonMissingEvidence, types.ReasonShortCircuited,
	} {
		if !seen[want] {
			t.Errorf("verdict fixtures do not cover %q", want)
		}
	}
}
