package policy_test

import (
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/policy"
)

func TestMissingInputs_ConfiguredRulesNeedTheirInput(t *testing.T) {
	yes := true
	score := 0.8
	cases := []struct {
		name         string
		pol          *policy.Policy
		haveBaseline bool
		haveMutation bool
		want         []string
	}{
		{"nil policy", nil, false, false, nil},
		{"no rules", &policy.Policy{}, false, false, nil},
		{"block_on_regression without baseline", &policy.Policy{BlockOnRegression: &yes}, false, false, []string{"--baseline"}},
		{"block_on_regression false", &policy.Policy{BlockOnRegression: new(bool)}, false, false, nil},
		{"regression rule without baseline", &policy.Policy{Regression: &policy.RegressionRule{}}, false, false, []string{"--baseline"}},
		{"regression rule with baseline", &policy.Policy{Regression: &policy.RegressionRule{}, BlockOnRegression: &yes}, true, false, nil},
		{"mutation rule without mutation", &policy.Policy{MinMutationScore: &policy.MutationRule{Score: &score}}, true, false, []string{"--mutation"}},
		{"mutation rule with mutation", &policy.Policy{MinMutationScore: &policy.MutationRule{Score: &score}}, false, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.MissingInputs(tc.pol, tc.haveBaseline, tc.haveMutation)
			if len(got) != len(tc.want) {
				t.Fatalf("MissingInputs = %v, want %d entries naming %v", got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("entry %d = %q, want it to name %s", i, got[i], w)
				}
			}
			if err := policy.MissingInputsError(tc.pol, tc.haveBaseline, tc.haveMutation); (err != nil) != (len(tc.want) > 0) {
				t.Errorf("MissingInputsError = %v, want error=%v", err, len(tc.want) > 0)
			}
		})
	}
}
