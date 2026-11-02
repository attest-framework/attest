package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-ai/attest/engine/internal/policy"
	"github.com/attest-ai/attest/engine/internal/report"
	"github.com/attest-ai/attest/engine/pkg/types"
)

func ptr[T any](v T) *T { return &v }

func TestEvaluate_PassesWhenNoRulesViolated(t *testing.T) {
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			1: {MaxHardFails: ptr(0)},
		},
		MaxCostUSD: ptr(1.0),
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusPass, Score: 1.0, Layer: 1},
	}
	r := policy.Evaluate(p, results, 0.05, nil)
	if !r.Passed() {
		t.Errorf("expected pass, got violations: %+v", r.Violations)
	}
	if r.ExitCode() != policy.ExitPass {
		t.Errorf("ExitCode = %d, want %d", r.ExitCode(), policy.ExitPass)
	}
}

func TestEvaluate_LayerHardFailLimitTriggersBlock(t *testing.T) {
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			1: {MaxHardFails: ptr(0)},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusHardFail, Layer: 1},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if r.Passed() {
		t.Fatalf("expected block, got pass")
	}
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("ExitCode = %d, want %d", r.ExitCode(), policy.ExitBlock)
	}
	if len(r.Violations) != 1 || r.Violations[0].Rule != "layer.1.max_hard_fails" {
		t.Errorf("unexpected violations: %+v", r.Violations)
	}
}

func TestEvaluate_LayerSeverityCanDowngradeToWarn(t *testing.T) {
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			1: {MaxHardFails: ptr(0), Severity: policy.SeverityWarn},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusHardFail, Layer: 1},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if r.ExitCode() != policy.ExitWarn {
		t.Errorf("ExitCode = %d, want ExitWarn (%d)", r.ExitCode(), policy.ExitWarn)
	}
}

func TestEvaluate_ExplicitZeroSoftFailsBlocksOnAnySoftFail(t *testing.T) {
	// max_soft_fails: 0 must block on any soft fail — pointer semantics
	// distinguish this from the unset case, which is silent.
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			6: {MaxSoftFails: ptr(0)},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeLLMJudge, Status: types.StatusSoftFail, Layer: 6},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("explicit max_soft_fails=0 should block; got exit %d", r.ExitCode())
	}
}

func TestEvaluate_UnsetSoftFailsLimitIsSilent(t *testing.T) {
	// MaxSoftFails == nil = no rule. A LayerLimit with only MaxHardFails
	// must NOT block on soft fails.
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			6: {MaxHardFails: ptr(0)},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeLLMJudge, Status: types.StatusSoftFail, Layer: 6},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if !r.Passed() {
		t.Errorf("unset max_soft_fails must be silent on soft fails; got %+v", r.Violations)
	}
}

func TestEvaluate_CostCeilingTriggersBlock(t *testing.T) {
	p := &policy.Policy{
		MaxCostUSD: ptr(0.10),
	}
	r := policy.Evaluate(p, nil, 0.50, nil)
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("ExitCode = %d, want %d (over-budget)", r.ExitCode(), policy.ExitBlock)
	}
	if len(r.Violations) != 1 || r.Violations[0].Rule != "max_cost_usd" {
		t.Errorf("expected one max_cost_usd violation, got %+v", r.Violations)
	}
}

func TestEvaluate_FailureClassLimitTriggersBlock(t *testing.T) {
	p := &policy.Policy{
		FailureClasses: map[string]policy.ClassLimit{
			types.FailureClassBrokenCode: {Max: ptr(0)},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusHardFail, Layer: 1, FailureClass: types.FailureClassBrokenCode},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("ExitCode = %d, want %d", r.ExitCode(), policy.ExitBlock)
	}
}

func TestEvaluate_FailureClassUnsetMaxIsSilent(t *testing.T) {
	// `failure_classes: { broken_code: {} }` decodes to ClassLimit{Max:nil}
	// which is "no rule" — must not block.
	p := &policy.Policy{
		FailureClasses: map[string]policy.ClassLimit{
			types.FailureClassBrokenCode: {},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusHardFail, Layer: 1, FailureClass: types.FailureClassBrokenCode},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if !r.Passed() {
		t.Errorf("class limit with nil Max must be silent; got %+v", r.Violations)
	}
}

func TestEvaluate_BlockOnRegression(t *testing.T) {
	p := &policy.Policy{
		BlockOnRegression: ptr(true),
	}
	delta := &report.BaselineDelta{
		BaselineTag: "v1",
		Regressions: []report.AssertionTransition{
			{AssertionID: "a1", BaselineStatus: types.StatusPass, CurrentStatus: types.StatusHardFail},
		},
	}
	r := policy.Evaluate(p, nil, 0, delta)
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("ExitCode = %d, want %d", r.ExitCode(), policy.ExitBlock)
	}
	if len(r.Violations) != 1 || r.Violations[0].Rule != "block_on_regression" {
		t.Errorf("expected block_on_regression violation, got %+v", r.Violations)
	}
}

func TestEvaluate_BlockOnRegressionIgnoredWhenNoBaseline(t *testing.T) {
	p := &policy.Policy{
		BlockOnRegression: ptr(true),
	}
	r := policy.Evaluate(p, nil, 0, nil)
	if !r.Passed() {
		t.Errorf("regression rule must be a no-op without a baseline; got %+v", r.Violations)
	}
}

func TestLoad_YAMLAndJSONDecodersAgree(t *testing.T) {
	dir := t.TempDir()

	yamlPath := filepath.Join(dir, "policy.yaml")
	yamlContent := `
layers:
  1:
    max_hard_fails: 0
  6:
    max_soft_fails: 5
    severity: warn
max_cost_usd: 0.50
block_on_regression: true
failure_classes:
  broken_code:
    max: 0
  flaky_judge:
    max: 3
    severity: warn
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	jsonPath := filepath.Join(dir, "policy.json")
	jsonContent := `{
		"layers": {
			"1": {"max_hard_fails": 0},
			"6": {"max_soft_fails": 5, "severity": "warn"}
		},
		"max_cost_usd": 0.50,
		"block_on_regression": true,
		"failure_classes": {
			"broken_code": {"max": 0},
			"flaky_judge": {"max": 3, "severity": "warn"}
		}
	}`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}

	yp, err := policy.Load(yamlPath)
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	jp, err := policy.Load(jsonPath)
	if err != nil {
		t.Fatalf("load json: %v", err)
	}

	for _, p := range []*policy.Policy{yp, jp} {
		if p.MaxCostUSD == nil || *p.MaxCostUSD != 0.50 {
			t.Errorf("max_cost_usd not loaded: %v", p.MaxCostUSD)
		}
		if p.BlockOnRegression == nil || !*p.BlockOnRegression {
			t.Errorf("block_on_regression not loaded")
		}
		if l1 := p.Layers[1]; l1.MaxHardFails == nil || *l1.MaxHardFails != 0 {
			t.Errorf("layer 1 max_hard_fails not loaded: %v", l1.MaxHardFails)
		}
		if l6 := p.Layers[6]; l6.Severity != "warn" {
			t.Errorf("layer 6 severity not loaded")
		}
		if l6 := p.Layers[6]; l6.MaxSoftFails == nil || *l6.MaxSoftFails != 5 {
			t.Errorf("layer 6 max_soft_fails not loaded: %v", l6.MaxSoftFails)
		}
		if cl := p.FailureClasses[types.FailureClassFlakyJudge]; cl.Max == nil || *cl.Max != 3 {
			t.Errorf("flaky_judge max not loaded: %v", cl.Max)
		}
	}
}

func TestLoad_RejectsBadSeverity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte("severity: blokc\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := policy.Load(path); err == nil {
		t.Error("expected error on misspelled severity, got nil")
	}
}

func TestLoad_RejectsOutOfRangeLayer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	yaml := `
layers:
  9:
    max_hard_fails: 0
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := policy.Load(path); err == nil {
		t.Error("expected error on layer 9, got nil")
	}
}

func TestLoad_RejectsNegativeCost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte("max_cost_usd: -0.5\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := policy.Load(path); err == nil {
		t.Error("expected error on negative max_cost_usd, got nil")
	}
}

func TestEvaluate_AggregateLayerCap(t *testing.T) {
	// Layer 0 is the global aggregate cap — it sums hard fails across
	// every real layer. Useful for "no more than 2 hard fails total".
	p := &policy.Policy{
		Layers: map[int]policy.LayerLimit{
			0: {MaxHardFails: ptr(1)},
		},
	}
	results := []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusHardFail, Layer: 1},
		{AssertionID: "a2", Type: types.TypeContent, Status: types.StatusHardFail, Layer: 4},
	}
	r := policy.Evaluate(p, results, 0, nil)
	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("layer 0 cap should aggregate; got exit %d", r.ExitCode())
	}
}

func notEvaluatedResults() []types.AssertionResult {
	return []types.AssertionResult{
		{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusPass, Layer: 1},
		{AssertionID: "j1", Type: types.TypeLLMJudge, Status: types.StatusNotEvaluated, Reason: types.ReasonProviderUnavailable, Layer: 6},
		{AssertionID: "j2", Type: types.TypeLLMJudge, Status: types.StatusNotEvaluated, Reason: types.ReasonProviderNotConfigured, Layer: 6},
	}
}

func TestEvaluate_NotEvaluatedWarnsByDefault(t *testing.T) {
	// Policy-wide block severity must not leak into not_evaluated.
	p := &policy.Policy{Severity: policy.SeverityBlock}

	r := policy.Evaluate(p, notEvaluatedResults(), 0, nil)

	if r.ExitCode() != policy.ExitWarn {
		t.Fatalf("ExitCode = %d, want %d; violations: %+v", r.ExitCode(), policy.ExitWarn, r.Violations)
	}
	v := r.Violations[0]
	if v.Rule != "not_evaluated" || v.Detail != "2 assertions not evaluated (provider_not_configured: 1, provider_unavailable: 1)" {
		t.Errorf("violation = %+v", v)
	}
}

func TestEvaluate_NotEvaluatedBlockWhenPolicySetsBlock(t *testing.T) {
	p := &policy.Policy{NotEvaluated: policy.SeverityBlock}

	r := policy.Evaluate(p, notEvaluatedResults(), 0, nil)

	if r.ExitCode() != policy.ExitBlock {
		t.Errorf("ExitCode = %d, want %d", r.ExitCode(), policy.ExitBlock)
	}
}

func TestEvaluate_NoNotEvaluatedRowsPasses(t *testing.T) {
	p := &policy.Policy{NotEvaluated: policy.SeverityBlock}
	results := []types.AssertionResult{{AssertionID: "a1", Type: types.TypeSchema, Status: types.StatusPass, Layer: 1}}

	r := policy.Evaluate(p, results, 0, nil)

	if !r.Passed() {
		t.Errorf("expected pass, got %+v", r.Violations)
	}
}

func TestLoad_RejectsBadNotEvaluatedSeverity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte("not_evaluated: ignore\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := policy.Load(path); err == nil {
		t.Error("expected error for not_evaluated: ignore")
	}
}

func TestEvaluate_UnknownStatusCountsAsNotEvaluated(t *testing.T) {
	results := []types.AssertionResult{{AssertionID: "x", Type: types.TypeSchema, Status: "error", Layer: 1}}

	r := policy.Evaluate(&policy.Policy{}, results, 0, nil)

	if r.ExitCode() != policy.ExitWarn {
		t.Fatalf("ExitCode = %d, want %d; violations: %+v", r.ExitCode(), policy.ExitWarn, r.Violations)
	}
	if want := `1 assertions not evaluated (unknown status "error": 1)`; r.Violations[0].Detail != want {
		t.Errorf("detail = %q, want %q", r.Violations[0].Detail, want)
	}
}

func TestValidate_RegressionOutOfRange_ReturnsError(t *testing.T) {
	cases := map[string]*policy.RegressionRule{
		"confidence 1":   {Confidence: ptr(1.0)},
		"confidence 0":   {Confidence: ptr(0.0)},
		"tolerance -0.1": {Tolerance: ptr(-0.1)},
		"tolerance 1":    {Tolerance: ptr(1.0)},
		"severity typo":  {Severity: "blokc"},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			p := &policy.Policy{Regression: rule}
			if err := p.Validate(); err == nil {
				t.Errorf("Validate(%+v) = nil", rule)
			}
		})
	}
}

func TestEvaluate_RegressionRuleWithoutMatchingIDs_IsInconclusive(t *testing.T) {
	p := &policy.Policy{Regression: &policy.RegressionRule{}}
	delta := report.ComputeBaselineDelta("base", nil, nil)
	results := []types.AssertionResult{{AssertionID: "new-id", Status: types.StatusPass}}

	got := policy.Evaluate(p, results, 0, &delta)

	if got.Verdict != report.TrialInconclusive || got.ExitCode() != policy.ExitWarn {
		t.Errorf("verdict=%q exit=%d, want inconclusive and exit 2", got.Verdict, got.ExitCode())
	}
}

func TestEvaluate_RegressionSeverityWarn_RegressedExitsTwo(t *testing.T) {
	p := &policy.Policy{Regression: &policy.RegressionRule{Severity: policy.SeverityWarn}}
	delta := report.BaselineDelta{BaselineTag: "base", BaselineCounts: map[string]report.PassCount{"a": {Passes: 20, Evaluated: 20}}}
	var results []types.AssertionResult
	for i := range 20 {
		status := types.StatusHardFail
		if i < 2 {
			status = types.StatusPass
		}
		results = append(results, types.AssertionResult{AssertionID: "a", Status: status, Trial: i + 1})
	}

	got := policy.Evaluate(p, results, 0, &delta)

	if got.Verdict != report.TrialRegressed || got.ExitCode() != policy.ExitWarn {
		t.Errorf("verdict=%q exit=%d, want regressed downgraded to exit 2", got.Verdict, got.ExitCode())
	}
}
