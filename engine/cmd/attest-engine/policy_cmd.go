package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/attest-ai/attest/engine/internal/policy"
	"github.com/attest-ai/attest/engine/internal/report"
)

// handlePolicyCommand dispatches `attest-engine policy <subcommand>`.
//
// Currently only one subcommand: `policy evaluate --policy <path>
// --report <path> [--baseline <tag>]`. Exits 0/1/2 matching
// policy.Result.ExitCode so a single CI step gates merges.
func handlePolicyCommand(args []string) {
	if len(args) == 0 {
		policyUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "evaluate":
		policyEvaluate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown policy subcommand: %s\n", args[0])
		policyUsage()
		os.Exit(2)
	}
}

func policyUsage() {
	fmt.Fprintln(os.Stderr, "usage: attest-engine policy evaluate --policy <path> --report <path> [--baseline <tag>]")
}

func policyEvaluate(args []string) {
	fs := flag.NewFlagSet("policy evaluate", flag.ExitOnError)
	policyPath := fs.String("policy", "", "path to attest.policy.yaml or .json")
	reportPath := fs.String("report", "", "path to JSON v2 report to evaluate")
	baselineTag := fs.String("baseline", "", "optional baseline tag for regression rules")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *policyPath == "" || *reportPath == "" {
		fmt.Fprintln(os.Stderr, "policy evaluate: --policy and --report are required")
		os.Exit(2)
	}

	pol, err := policy.Load(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
		os.Exit(1)
	}

	env, err := loadReport(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
		os.Exit(1)
	}

	var delta *report.BaselineDelta
	if *baselineTag != "" {
		delta, err = computeBaselineDelta(*baselineTag, env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy evaluate: %v\n", err)
			os.Exit(1)
		}
	}

	result := policy.Evaluate(pol, env.Results, env.TotalCost, delta)

	emitJSON(map[string]any{
		"violations": result.Violations,
		"exit_code":  result.ExitCode(),
		"passed":     result.Passed(),
	})
	os.Exit(result.ExitCode())
}
