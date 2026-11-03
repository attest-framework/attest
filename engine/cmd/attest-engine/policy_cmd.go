package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/attest-ai/attest/engine/internal/policy"
	"github.com/attest-ai/attest/engine/internal/report"
)

func init() {
	register(command{
		name:    "policy",
		summary: "evaluate a report against a policy (evaluate); exits 0 pass, 1 block, 2 warn, 3 usage or load error",
		run:     handlePolicyCommand,
		subs: []subcommand{
			{name: "evaluate", schema: "cli/policy-evaluate.schema.json"},
		},
		exits: []exitCode{
			{policy.ExitPass, "pass: every rule satisfied"},
			{policy.ExitBlock, "block: a block-severity rule was violated"},
			{policy.ExitWarn, "warn: only warn-severity rules were violated"},
			{policy.ExitUsage, "usage error, or the policy, report, baseline or mutation report failed to load, or the policy sets min_mutation_score without --mutation"},
		},
	})
}

// handlePolicyCommand dispatches `attest-engine policy <subcommand>`.
//
// Currently only one subcommand: `policy evaluate --policy <path>
// --report <path> [--baseline <tag>] [--json]`. Exits 0/1/2 matching
// policy.Result.ExitCode so a single CI step gates merges, and
// policy.ExitUsage when it cannot evaluate.
func handlePolicyCommand(args []string) {
	if len(args) == 0 {
		policyUsage()
		os.Exit(policy.ExitUsage)
	}
	switch args[0] {
	case "evaluate":
		policyEvaluate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown policy subcommand: %s\n", args[0])
		policyUsage()
		os.Exit(policy.ExitUsage)
	}
}

func policyUsage() {
	fmt.Fprintln(os.Stderr, "usage: attest-engine policy evaluate --policy <path> --report <path> [--baseline <tag>] [--mutation <path>] [--json]")
}

func policyEvaluate(args []string) {
	fs := flag.NewFlagSet("policy evaluate", flag.ContinueOnError)
	policyPath := fs.String("policy", "", "path to attest.policy.yaml or .json")
	reportPath := fs.String("report", "", "path to JSON v2 report to evaluate")
	baselineTag := fs.String("baseline", "", "optional baseline tag for regression rules")
	mutationPath := fs.String("mutation", "", "mutate --json output, for the min_mutation_score rule")
	fs.Bool("json", false, "print the result as JSON (always on)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(exitOK)
		}
		os.Exit(policy.ExitUsage)
	}
	if *policyPath == "" || *reportPath == "" || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "policy evaluate: --policy and --report are required")
		policyUsage()
		os.Exit(policy.ExitUsage)
	}

	pol, err := policy.Load(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
		os.Exit(policy.ExitUsage)
	}

	env, err := loadReport(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
		os.Exit(policy.ExitUsage)
	}

	var delta *report.BaselineDelta
	if *baselineTag != "" {
		delta, err = computeBaselineDelta(*baselineTag, env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
			os.Exit(policy.ExitUsage)
		}
	}

	var mutationScore *float64
	if *mutationPath != "" {
		mutationScore, err = loadMutationScore(*mutationPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
			os.Exit(policy.ExitUsage)
		}
	} else if pol.MinMutationScore != nil {
		fmt.Fprintln(os.Stderr, "policy evaluate: the policy sets min_mutation_score; pass --mutation <path> (the output of `attest mutate --json`)")
		os.Exit(policy.ExitUsage)
	}

	result := policy.Evaluate(pol, env.Results, env.TotalCost, delta)
	if v, ok := policy.MutationViolation(pol, mutationScore); ok {
		result.Violations = append(result.Violations, v)
	}

	out := map[string]any{
		"violations": result.Violations,
		"exit_code":  result.ExitCode(),
		"passed":     result.Passed(),
	}
	if result.Verdict != "" {
		out["verdict"] = result.Verdict
		out["trials"] = result.Trials
	}
	emitJSON(out)
	os.Exit(result.ExitCode())
}

// loadMutationScore reads the score from `attest mutate --json` output. The
// score is null when no mutant had an applicable contract; a document
// without the key is not mutate output.
func loadMutationScore(path string) (*float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mutation report %s: %w", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode mutation report %s: %w", path, err)
	}
	raw, ok := doc["score"]
	if !ok {
		return nil, fmt.Errorf("mutation report %s has no \"score\" field; pass the output of `attest mutate --json`", path)
	}
	var score *float64
	if err := json.Unmarshal(raw, &score); err != nil {
		return nil, fmt.Errorf("mutation report %s: score must be a number or null: %w", path, err)
	}
	if score != nil && (*score < 0 || *score > 1) {
		return nil, fmt.Errorf("mutation report %s: score %g is outside [0, 1]", path, *score)
	}
	return score, nil
}
