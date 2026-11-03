package policy

import (
	"path/filepath"
	"strings"
	"testing"

	"os"
)

func ptr(f float64) *float64 { return &f }

func TestMutationViolation_ScoreBelowThreshold_ReportsBlock(t *testing.T) {
	p := &Policy{MinMutationScore: &MutationRule{Score: ptr(0.8)}}

	v, ok := MutationViolation(p, ptr(0.5))

	if !ok || v.Rule != "min_mutation_score" || v.Severity != SeverityBlock {
		t.Fatalf("got %+v, %v", v, ok)
	}
	if !strings.Contains(v.Detail, "0.5") || !strings.Contains(v.Detail, "0.8") {
		t.Errorf("detail %q lacks the score and the threshold", v.Detail)
	}
}

func TestMutationViolation_ScoreAtOrAboveThreshold_NoViolation(t *testing.T) {
	p := &Policy{MinMutationScore: &MutationRule{Score: ptr(0.8)}}
	for _, score := range []float64{0.8, 0.95, 1} {
		if v, ok := MutationViolation(p, ptr(score)); ok {
			t.Errorf("score %g: %+v", score, v)
		}
	}
}

func TestMutationViolation_Severity_RuleThenPolicyThenBlock(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		want string
	}{
		{"rule warn", Policy{MinMutationScore: &MutationRule{Score: ptr(0.8), Severity: SeverityWarn}}, SeverityWarn},
		{"policy warn", Policy{Severity: SeverityWarn, MinMutationScore: &MutationRule{Score: ptr(0.8)}}, SeverityWarn},
		{"rule beats policy", Policy{Severity: SeverityWarn, MinMutationScore: &MutationRule{Score: ptr(0.8), Severity: SeverityBlock}}, SeverityBlock},
		{"default", Policy{MinMutationScore: &MutationRule{Score: ptr(0.8)}}, SeverityBlock},
	}
	for _, c := range cases {
		v, ok := MutationViolation(&c.p, ptr(0.1))
		if !ok || v.Severity != c.want {
			t.Errorf("%s: %+v %v, want %s", c.name, v, ok, c.want)
		}
	}
}

func TestMutationViolation_NoScore_IsAViolation(t *testing.T) {
	p := &Policy{MinMutationScore: &MutationRule{Score: ptr(0.8), Severity: SeverityWarn}}

	v, ok := MutationViolation(p, nil)

	if !ok || v.Severity != SeverityWarn || !strings.Contains(v.Detail, "no mutation score") {
		t.Fatalf("got %+v, %v", v, ok)
	}
}

func TestMutationViolation_RuleAbsent_NoEffect(t *testing.T) {
	if v, ok := MutationViolation(&Policy{}, ptr(0)); ok {
		t.Errorf("rule absent but got %+v", v)
	}
	if _, ok := MutationViolation(nil, ptr(0)); ok {
		t.Error("nil policy produced a violation")
	}
}

func TestValidate_MinMutationScore_RejectsInvalidRules(t *testing.T) {
	cases := map[string]*MutationRule{
		"missing score": {},
		"above one":     {Score: ptr(1.5)},
		"negative":      {Score: ptr(-0.1)},
		"bad severity":  {Score: ptr(0.5), Severity: "blokc"},
	}
	for name, r := range cases {
		p := Policy{MinMutationScore: r}
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, ok := range []float64{0, 0.8, 1} {
		p := Policy{MinMutationScore: &MutationRule{Score: ptr(ok)}}
		if err := p.Validate(); err != nil {
			t.Errorf("score %g: %v", ok, err)
		}
	}
}

func TestLoad_MinMutationScoreYAML_ParsesScoreAndSeverity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte("min_mutation_score:\n  score: 0.8\n  severity: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)

	if err != nil || p.MinMutationScore == nil || *p.MinMutationScore.Score != 0.8 || p.MinMutationScore.Severity != SeverityWarn {
		t.Fatalf("got %+v, %v", p, err)
	}
}
