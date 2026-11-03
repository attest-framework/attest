// Package policy evaluates an attest.policy.yaml against an evaluation
// run, producing a typed Result with violations and an exit code that
// CI can use to gate merges.
//
// The policy schema is intentionally small (per-layer thresholds, cost
// ceiling, regression rules, failure-class blocking) so a reviewer can
// scan a 20-line YAML file rather than hunt through code for what
// blocks a merge.
package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/attest-ai/attest/engine/internal/report"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// Exit codes returned by Result.ExitCode. The engine and CLI map these
// directly to process exit codes so a single `if attest report --policy
// attest.policy.yaml` line in a workflow gates merges.
const (
	// ExitPass means every rule in the policy was satisfied.
	ExitPass = 0
	// ExitBlock means at least one Severity=block rule was violated and
	// the merge must be stopped.
	ExitBlock = 1
	// ExitWarn means only Severity=warn rules were violated. CI may
	// surface the warning without blocking.
	ExitWarn = 2
	// ExitUsage means `policy evaluate` could not run: a usage error, or a
	// policy, report or baseline that failed to load. It is distinct from the
	// three verdicts so a CI gate never mistakes broken input for a warning.
	ExitUsage = 3
)

// SeverityBlock and SeverityWarn are the two rule severities. Anything
// else in the YAML is rejected at load time so a typo can't silently
// downgrade a blocking rule.
const (
	SeverityBlock = "block"
	SeverityWarn  = "warn"
)

// Policy is the on-disk contract loaded from attest.policy.yaml or .json.
//
//   - Layers caps hard/soft fails per pipeline layer (1–8). Layer 0 is
//     reserved for "any layer" / global caps.
//   - MaxCostUSD blocks runs that exceed an absolute spend.
//   - BlockOnRegression turns any baseline regression into a blocking
//     violation. Combined with `attest report --vs <tag>` this gates
//     merges that would lower the pass rate.
//   - FailureClasses sets per-class limits ("broken_code: 0" means
//     the policy refuses to ship with broken-code failures even if
//     soft-fail budgets would otherwise allow them).
//   - Severity defaults to "block" for individual rules; setting it
//     to "warn" downgrades the violation to ExitWarn.
//   - NotEvaluated sets the severity for not_evaluated assertions
//     (provider outage, missing key, simulation, ...). Default "warn".
type Policy struct {
	Layers            map[int]LayerLimit    `yaml:"layers" json:"layers,omitempty"`
	MaxCostUSD        *float64              `yaml:"max_cost_usd" json:"max_cost_usd,omitempty"`
	BlockOnRegression *bool                 `yaml:"block_on_regression" json:"block_on_regression,omitempty"`
	FailureClasses    map[string]ClassLimit `yaml:"failure_classes" json:"failure_classes,omitempty"`
	Severity          string                `yaml:"severity" json:"severity,omitempty"`
	// NotEvaluated is the severity applied when any assertion has status
	// not_evaluated: "warn" (default, exit 2) or "block" (exit 1). It does
	// not inherit Severity, so an outage never blocks unless asked to.
	NotEvaluated string `yaml:"not_evaluated" json:"not_evaluated,omitempty"`
	// Regression compares each assertion's pass rate over fixed-N trials
	// with the baseline (`--baseline <tag>`) using a Wilson interval:
	// regressed (Severity, default block → exit 1), inconclusive (warn →
	// exit 2) or pass. `regression: {}` applies the defaults.
	Regression *RegressionRule `yaml:"regression" json:"regression,omitempty"`
	// MinMutationScore requires the mutation score from `attest mutate`
	// (`policy evaluate --mutation <mutate.json>`) to reach a threshold.
	MinMutationScore *MutationRule `yaml:"min_mutation_score" json:"min_mutation_score,omitempty"`
}

// MutationRule is the min_mutation_score rule: Score is the lowest
// acceptable killed/(killed+survived) ratio, in [0, 1]. Severity defaults to
// the policy severity, then to block.
type MutationRule struct {
	Score    *float64 `yaml:"score" json:"score,omitempty"`
	Severity string   `yaml:"severity" json:"severity,omitempty"`
}

// RegressionRule configures the statistical baseline comparison. Nil
// fields take report.DefaultConfidence (0.95) and report.DefaultTolerance
// (0.05). Severity applies to regressed assertions; inconclusive ones
// always warn.
type RegressionRule struct {
	Confidence *float64 `yaml:"confidence" json:"confidence,omitempty"`
	Tolerance  *float64 `yaml:"tolerance" json:"tolerance,omitempty"`
	Severity   string   `yaml:"severity" json:"severity,omitempty"`
}

// resolved returns the rule with defaults applied.
func (r *RegressionRule) resolved() report.RegressionRule {
	out := report.RegressionRule{Confidence: report.DefaultConfidence, Tolerance: report.DefaultTolerance}
	if r.Confidence != nil {
		out.Confidence = *r.Confidence
	}
	if r.Tolerance != nil {
		out.Tolerance = *r.Tolerance
	}
	return out
}

// LayerLimit is a per-layer ceiling on hard or soft fails. Both caps
// are pointers so an unset field (no rule) is distinguishable from an
// explicit zero (block on any failure of that severity). Mirrors the
// MaxCostUSD pattern.
type LayerLimit struct {
	MaxHardFails *int   `yaml:"max_hard_fails" json:"max_hard_fails,omitempty"`
	MaxSoftFails *int   `yaml:"max_soft_fails" json:"max_soft_fails,omitempty"`
	Severity     string `yaml:"severity" json:"severity,omitempty"`
}

// ClassLimit caps the number of assertions tagged with a given
// FailureClass (broken_code, flaky_judge, ...). Max is a pointer so
// `failure_classes: { broken_code: {} }` (no rule) is distinguishable
// from `failure_classes: { broken_code: {max: 0} }` (block on any).
type ClassLimit struct {
	Max      *int   `yaml:"max" json:"max,omitempty"`
	Severity string `yaml:"severity" json:"severity,omitempty"`
}

// Violation is one breach of the policy. Severity propagates to the
// final exit code.
type Violation struct {
	Rule     string `json:"rule"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
}

// Result is the outcome of evaluating a policy against a report. Verdict
// and Trials are set when the policy has a regression rule and a baseline
// was given: Verdict is pass, regressed or inconclusive.
type Result struct {
	Violations []Violation           `json:"violations"`
	Verdict    string                `json:"verdict,omitempty"`
	Trials     []report.TrialVerdict `json:"trials,omitempty"`
}

// ExitCode returns 0 when no violations exist, 1 when any blocking
// violation exists, otherwise 2.
func (r *Result) ExitCode() int {
	hasBlock := false
	hasWarn := false
	for _, v := range r.Violations {
		switch v.Severity {
		case SeverityBlock:
			hasBlock = true
		case SeverityWarn:
			hasWarn = true
		}
	}
	if hasBlock {
		return ExitBlock
	}
	if hasWarn {
		return ExitWarn
	}
	return ExitPass
}

// Passed reports whether the policy was fully satisfied.
func (r *Result) Passed() bool {
	return r.ExitCode() == ExitPass
}

// Load reads and validates a policy from path. Supports YAML
// (`.yaml`/`.yml`) and JSON (`.json`); falls back to YAML for any other
// extension because YAML accepts JSON.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy %s: %w", path, err)
	}
	var p Policy
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("decode policy %s as JSON: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("decode policy %s as YAML: %w", path, err)
		}
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid policy %s: %w", path, err)
	}
	return &p, nil
}

// Validate checks the policy for self-consistent severity values. Empty
// policies are valid (the engine treats them as "no rules"); typos in
// severity are rejected so a misspelled "blokc" cannot silently default.
func (p *Policy) Validate() error {
	if err := validateSeverity("policy", p.Severity); err != nil {
		return err
	}
	if err := validateSeverity("not_evaluated", p.NotEvaluated); err != nil {
		return err
	}
	for layer, lim := range p.Layers {
		if layer < 0 || layer > 8 {
			return fmt.Errorf("layer key %d out of range (must be 0–8)", layer)
		}
		if lim.MaxHardFails != nil && *lim.MaxHardFails < 0 {
			return fmt.Errorf("layer %d: max_hard_fails must be non-negative", layer)
		}
		if lim.MaxSoftFails != nil && *lim.MaxSoftFails < 0 {
			return fmt.Errorf("layer %d: max_soft_fails must be non-negative", layer)
		}
		if err := validateSeverity(fmt.Sprintf("layer %d", layer), lim.Severity); err != nil {
			return err
		}
	}
	if p.MaxCostUSD != nil && *p.MaxCostUSD < 0 {
		return fmt.Errorf("max_cost_usd must be non-negative; got %f", *p.MaxCostUSD)
	}
	if r := p.Regression; r != nil {
		for name, v := range map[string]*float64{"confidence": r.Confidence, "tolerance": r.Tolerance} {
			if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
				return fmt.Errorf("regression.%s must be a finite number; got %g", name, *v)
			}
		}
		if r.Confidence != nil && (*r.Confidence <= 0 || *r.Confidence >= 1) {
			return fmt.Errorf("regression.confidence must be between 0 and 1 exclusive; got %g", *r.Confidence)
		}
		if r.Tolerance != nil && (*r.Tolerance < 0 || *r.Tolerance >= 1) {
			return fmt.Errorf("regression.tolerance must be in [0, 1); got %g", *r.Tolerance)
		}
		if err := validateSeverity("regression", r.Severity); err != nil {
			return err
		}
	}
	if r := p.MinMutationScore; r != nil {
		if r.Score == nil {
			return fmt.Errorf("min_mutation_score.score is required")
		}
		if math.IsNaN(*r.Score) || *r.Score < 0 || *r.Score > 1 {
			return fmt.Errorf("min_mutation_score.score must be between 0 and 1; got %g", *r.Score)
		}
		if err := validateSeverity("min_mutation_score", r.Severity); err != nil {
			return err
		}
	}
	for class, lim := range p.FailureClasses {
		if lim.Max != nil && *lim.Max < 0 {
			return fmt.Errorf("failure class %q: max must be non-negative", class)
		}
		if err := validateSeverity(fmt.Sprintf("failure_class %s", class), lim.Severity); err != nil {
			return err
		}
	}
	return nil
}

func validateSeverity(field, severity string) error {
	switch severity {
	case "", SeverityBlock, SeverityWarn:
		return nil
	}
	return fmt.Errorf("%s: severity %q must be %q, %q, or empty", field, severity, SeverityBlock, SeverityWarn)
}

// resolvedSeverity picks the rule severity, falling back to the policy
// default, then to "block".
func resolvedSeverity(rule, policyDefault string) string {
	if rule != "" {
		return rule
	}
	if policyDefault != "" {
		return policyDefault
	}
	return SeverityBlock
}

// Evaluate applies the policy to a result set and an optional baseline
// delta. Returns the violation list inside Result. The current cost is
// taken as totalCost so callers can pass either the engine-reported
// total or a recomputed sum.
func Evaluate(p *Policy, results []types.AssertionResult, totalCost float64, baseline *report.BaselineDelta) Result {
	var out Result
	if p == nil {
		return out
	}

	// ── Per-layer limits ──
	if len(p.Layers) > 0 {
		hardByLayer := make(map[int]int)
		softByLayer := make(map[int]int)
		for _, r := range results {
			layer := r.Layer
			if layer == 0 {
				layer = types.LayerForType(r.Type)
			}
			switch r.Status {
			case types.StatusHardFail:
				hardByLayer[layer]++
				hardByLayer[0]++
			case types.StatusSoftFail:
				softByLayer[layer]++
				softByLayer[0]++
			}
		}
		for layer, lim := range p.Layers {
			sev := resolvedSeverity(lim.Severity, p.Severity)
			if lim.MaxHardFails != nil {
				if got := hardByLayer[layer]; got > *lim.MaxHardFails {
					out.Violations = append(out.Violations, Violation{
						Rule:     fmt.Sprintf("layer.%d.max_hard_fails", layer),
						Detail:   fmt.Sprintf("got %d hard failures on layer %d (limit %d)", got, layer, *lim.MaxHardFails),
						Severity: sev,
					})
				}
			}
			if lim.MaxSoftFails != nil {
				if got := softByLayer[layer]; got > *lim.MaxSoftFails {
					out.Violations = append(out.Violations, Violation{
						Rule:     fmt.Sprintf("layer.%d.max_soft_fails", layer),
						Detail:   fmt.Sprintf("got %d soft failures on layer %d (limit %d)", got, layer, *lim.MaxSoftFails),
						Severity: sev,
					})
				}
			}
		}
	}

	// ── Cost ceiling ──
	if p.MaxCostUSD != nil && totalCost > *p.MaxCostUSD {
		out.Violations = append(out.Violations, Violation{
			Rule:     "max_cost_usd",
			Detail:   fmt.Sprintf("total cost $%.6f exceeds limit $%.6f", totalCost, *p.MaxCostUSD),
			Severity: resolvedSeverity("", p.Severity),
		})
	}

	// ── Failure-class limits ──
	if len(p.FailureClasses) > 0 {
		counts := make(map[string]int)
		for _, r := range results {
			if r.FailureClass != "" {
				counts[r.FailureClass]++
			}
		}
		for class, lim := range p.FailureClasses {
			if lim.Max == nil {
				continue
			}
			if got := counts[class]; got > *lim.Max {
				out.Violations = append(out.Violations, Violation{
					Rule:     fmt.Sprintf("failure_class.%s", class),
					Detail:   fmt.Sprintf("got %d %s failures (limit %d)", got, class, *lim.Max),
					Severity: resolvedSeverity(lim.Severity, p.Severity),
				})
			}
		}
	}

	// ── Not-evaluated block ──
	if v, ok := notEvaluatedViolation(results, p.NotEvaluated); ok {
		out.Violations = append(out.Violations, v)
	}

	// ── Statistical regression (fixed-N trials) ──
	compared := evaluateTrials(p, &out, results, baseline)

	// ── Regression block ──
	// Assertions the Wilson rule compared are judged by it alone: their
	// folded worst status would call one failing trial in twenty a
	// regression. Every other assertion keeps the status comparison.
	if p.BlockOnRegression != nil && *p.BlockOnRegression && baseline != nil {
		if ids := statusRegressions(baseline, compared); len(ids) > 0 {
			out.Violations = append(out.Violations, Violation{
				Rule:     "block_on_regression",
				Detail:   fmt.Sprintf("regressions vs %s: %s", baseline.BaselineTag, strings.Join(ids, ", ")),
				Severity: resolvedSeverity("", p.Severity),
			})
		}
	}

	return out
}

// trialViolations reports regressed assertions at severity and
// inconclusive ones as a warning.
func trialViolations(trials []report.TrialVerdict, tag, severity string) []Violation {
	var regressed, inconclusive []string
	for _, t := range trials {
		switch t.Verdict {
		case report.TrialRegressed:
			regressed = append(regressed, t.String())
		case report.TrialInconclusive:
			inconclusive = append(inconclusive, t.String())
		}
	}
	var out []Violation
	if len(regressed) > 0 {
		out = append(out, Violation{
			Rule:     "regression",
			Detail:   fmt.Sprintf("regressed vs %s: %s", tag, strings.Join(regressed, "; ")),
			Severity: severity,
		})
	}
	if len(inconclusive) > 0 {
		out = append(out, Violation{
			Rule:     "regression.inconclusive",
			Detail:   fmt.Sprintf("inconclusive vs %s (run more trials): %s", tag, strings.Join(inconclusive, "; ")),
			Severity: SeverityWarn,
		})
	}
	return out
}

// notEvaluatedViolation reports a "not_evaluated" violation when any
// result has status not_evaluated (or an unknown status), with per-reason
// counts in Detail.
// severity is the policy's not_evaluated setting; empty means warn.
func notEvaluatedViolation(results []types.AssertionResult, severity string) (Violation, bool) {
	byReason := make(map[string]int)
	total := 0
	for _, r := range results {
		switch r.Status {
		case types.StatusPass, types.StatusSoftFail, types.StatusHardFail:
		case types.StatusNotEvaluated:
			byReason[r.Reason]++
			total++
		default:
			// An unknown status is never a pass: count it as not evaluated.
			byReason[fmt.Sprintf("unknown status %q", r.Status)]++
			total++
		}
	}
	if total == 0 {
		return Violation{}, false
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, len(reasons))
	for i, reason := range reasons {
		parts[i] = fmt.Sprintf("%s: %d", reason, byReason[reason])
	}
	if severity == "" {
		severity = SeverityWarn
	}
	return Violation{
		Rule:     "not_evaluated",
		Detail:   fmt.Sprintf("%d assertions not evaluated (%s)", total, strings.Join(parts, ", ")),
		Severity: severity,
	}, true
}

// evaluateTrials applies the regression rule: assertions with at least
// report.MinTrialRuns runs in both the report and the baseline get a
// Wilson verdict. Assertions run as trials that cannot be compared
// (baseline pinned from a single run, or not evaluated on one side) are
// reported inconclusive, so they never pass silently. Returns the ids the
// rule compared.
func evaluateTrials(p *Policy, out *Result, results []types.AssertionResult, baseline *report.BaselineDelta) map[string]bool {
	compared := make(map[string]bool)
	if p.Regression == nil {
		return compared
	}
	if baseline == nil {
		out.Verdict = report.TrialInconclusive
		out.Violations = append(out.Violations, Violation{
			Rule:     "regression.inconclusive",
			Detail:   "the policy has a regression rule but no baseline was given; pass --baseline <tag>",
			Severity: SeverityWarn,
		})
		return compared
	}
	current := report.CountPasses(results)
	out.Trials = report.CompareTrials(current, baseline.BaselineCounts, p.Regression.resolved())
	for _, t := range out.Trials {
		compared[t.AssertionID] = true
	}
	var uncompared []string
	for id, c := range current {
		if c.Runs() >= report.MinTrialRuns && !compared[id] {
			uncompared = append(uncompared, id)
		}
	}
	sort.Strings(uncompared)
	if len(out.Trials) == 0 && len(uncompared) == 0 {
		return compared // single runs only: the status comparison decides
	}
	out.Verdict = report.OverallTrialVerdict(out.Trials)
	out.Violations = append(out.Violations, trialViolations(out.Trials, baseline.BaselineTag,
		resolvedSeverity(p.Regression.Severity, p.Severity))...)
	if len(uncompared) > 0 {
		if out.Verdict == report.TrialPass {
			out.Verdict = report.TrialInconclusive
		}
		out.Violations = append(out.Violations, Violation{
			Rule: "regression.inconclusive",
			Detail: fmt.Sprintf("not compared with %s (needs %d+ runs evaluated on both sides; status rules still apply): %s",
				baseline.BaselineTag, report.MinTrialRuns, strings.Join(uncompared, ", ")),
			Severity: SeverityWarn,
		})
	}
	return compared
}

// statusRegressions returns the baseline status regressions of the
// assertions the Wilson rule did not compare.
func statusRegressions(baseline *report.BaselineDelta, compared map[string]bool) []string {
	var out []string
	for _, id := range baseline.RegressedAssertionIDs() {
		if !compared[id] {
			out = append(out, id)
		}
	}
	return out
}

// MutationViolation applies the min_mutation_score rule to the score from
// `attest mutate`. A nil score (no mutant had an applicable contract) cannot
// meet a threshold and is a violation. It reports no violation when the
// policy has no such rule.
func MutationViolation(p *Policy, score *float64) (Violation, bool) {
	if p == nil || p.MinMutationScore == nil {
		return Violation{}, false
	}
	r := p.MinMutationScore
	sev := resolvedSeverity(r.Severity, p.Severity)
	switch {
	case score == nil:
		return Violation{
			Rule:     "min_mutation_score",
			Detail:   fmt.Sprintf("no mutation score (no mutant was scored); required at least %g", *r.Score),
			Severity: sev,
		}, true
	case *score < *r.Score:
		return Violation{
			Rule:     "min_mutation_score",
			Detail:   fmt.Sprintf("mutation score %.4g is below the required %g", *score, *r.Score),
			Severity: sev,
		}, true
	}
	return Violation{}, false
}
