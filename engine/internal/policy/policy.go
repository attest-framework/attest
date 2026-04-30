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
	"os"
	"path/filepath"
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
type Policy struct {
	Layers            map[int]LayerLimit    `yaml:"layers" json:"layers,omitempty"`
	MaxCostUSD        *float64              `yaml:"max_cost_usd" json:"max_cost_usd,omitempty"`
	BlockOnRegression *bool                 `yaml:"block_on_regression" json:"block_on_regression,omitempty"`
	FailureClasses    map[string]ClassLimit `yaml:"failure_classes" json:"failure_classes,omitempty"`
	Severity          string                `yaml:"severity" json:"severity,omitempty"`
}

// LayerLimit is a per-layer ceiling on hard or soft fails.
type LayerLimit struct {
	MaxHardFails int    `yaml:"max_hard_fails" json:"max_hard_fails"`
	MaxSoftFails int    `yaml:"max_soft_fails" json:"max_soft_fails,omitempty"`
	Severity     string `yaml:"severity" json:"severity,omitempty"`
}

// ClassLimit caps the number of assertions tagged with a given
// FailureClass (broken_code, flaky_judge, ...).
type ClassLimit struct {
	Max      int    `yaml:"max" json:"max"`
	Severity string `yaml:"severity" json:"severity,omitempty"`
}

// Violation is one breach of the policy. Severity propagates to the
// final exit code.
type Violation struct {
	Rule     string `json:"rule"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
}

// Result is the outcome of evaluating a policy against a report.
type Result struct {
	Violations []Violation `json:"violations"`
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
	for layer, lim := range p.Layers {
		if layer < 0 || layer > 8 {
			return fmt.Errorf("layer key %d out of range (must be 0–8)", layer)
		}
		if lim.MaxHardFails < 0 || lim.MaxSoftFails < 0 {
			return fmt.Errorf("layer %d: limits must be non-negative", layer)
		}
		if err := validateSeverity(fmt.Sprintf("layer %d", layer), lim.Severity); err != nil {
			return err
		}
	}
	if p.MaxCostUSD != nil && *p.MaxCostUSD < 0 {
		return fmt.Errorf("max_cost_usd must be non-negative; got %f", *p.MaxCostUSD)
	}
	for class, lim := range p.FailureClasses {
		if lim.Max < 0 {
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
			if got := hardByLayer[layer]; got > lim.MaxHardFails {
				out.Violations = append(out.Violations, Violation{
					Rule:     fmt.Sprintf("layer.%d.max_hard_fails", layer),
					Detail:   fmt.Sprintf("got %d hard failures on layer %d (limit %d)", got, layer, lim.MaxHardFails),
					Severity: sev,
				})
			}
			if lim.MaxSoftFails > 0 {
				if got := softByLayer[layer]; got > lim.MaxSoftFails {
					out.Violations = append(out.Violations, Violation{
						Rule:     fmt.Sprintf("layer.%d.max_soft_fails", layer),
						Detail:   fmt.Sprintf("got %d soft failures on layer %d (limit %d)", got, layer, lim.MaxSoftFails),
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
			if got := counts[class]; got > lim.Max {
				out.Violations = append(out.Violations, Violation{
					Rule:     fmt.Sprintf("failure_class.%s", class),
					Detail:   fmt.Sprintf("got %d %s failures (limit %d)", got, class, lim.Max),
					Severity: resolvedSeverity(lim.Severity, p.Severity),
				})
			}
		}
	}

	// ── Regression block ──
	if p.BlockOnRegression != nil && *p.BlockOnRegression && baseline != nil {
		if ids := baseline.RegressedAssertionIDs(); len(ids) > 0 {
			out.Violations = append(out.Violations, Violation{
				Rule:     "block_on_regression",
				Detail:   fmt.Sprintf("regressions vs %s: %s", baseline.BaselineTag, strings.Join(ids, ", ")),
				Severity: resolvedSeverity("", p.Severity),
			})
		}
	}

	return out
}
