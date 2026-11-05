package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/attest-framework/attest/engine/internal/capture"
	"github.com/attest-framework/attest/engine/internal/contractlog"
	"github.com/attest-framework/attest/engine/internal/mutate"
)

func init() {
	register(command{
		name:    "mutate",
		summary: "score a suite's contracts against behavior mutants of fixtures: <fixture.attest.json...> [--contracts <dir>] [--seed N] [--max-mutants N] [--json]",
		run:     func(args []string) { os.Exit(runMutate(args, os.Stdout, os.Stderr)) },
		schema:  "cli/mutate.schema.json",
		exits: []exitCode{
			{exitOK, "a mutation report was produced"},
			{exitFailure, "no contract applies to any fixture, so there is no score"},
			{exitUsage, "usage error, or a fixture or contracts directory could not be loaded"},
		},
	})
}

const mutateUsage = "usage: attest-engine mutate <fixture.attest.json...> [--contracts <dir>] [--seed N] [--max-mutants N] [--json]"

// runMutate builds mutants of each fixture, evaluates the suite's contracts
// on every one, and prints the score, the survivors and a contract that kills
// each. Contracts come from --contracts (specs recorded with
// ATTEST_CONTRACTS_DIR) or, without it, from the fixtures' drafted contracts.
func runMutate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contracts := fs.String("contracts", "", "directory of specs recorded with ATTEST_CONTRACTS_DIR (default: each fixture's drafted contracts)")
	seed := fs.Uint64("seed", 0, "seed for the sample taken when --max-mutants caps the set")
	maxMutants := fs.Int("max-mutants", 0, "cap the number of mutants; 0 keeps all")
	asJSON := fs.Bool("json", false, "print the report as JSON")

	// Flags may follow the fixture paths, so parse repeatedly. Everything
	// after a bare -- is a path.
	var paths, afterDashes []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, afterDashes = args[:i], args[i+1:]
	}
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitOK
			}
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		paths = append(paths, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	paths = append(paths, afterDashes...)
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "mutate: at least one fixture is required")
		fmt.Fprintln(stderr, mutateUsage)
		return exitUsage
	}
	if *maxMutants < 0 {
		fmt.Fprintln(stderr, "mutate: --max-mutants must be >= 0")
		return exitUsage
	}

	var subjects []mutate.Subject
	var drafted []mutate.Spec
	for _, p := range paths {
		fx, err := capture.LoadFixture(p)
		if err != nil {
			fmt.Fprintf(stderr, "mutate: %s\n", terminalSafe(err.Error()))
			return exitUsage
		}
		subjects = append(subjects, mutate.Subject{Name: fx.Name, Trace: fx.Trace, World: fx.World})
		for _, c := range fx.Contracts {
			drafted = append(drafted, mutate.Spec{AssertionID: c.ID, Type: c.Type, Spec: c.Spec})
		}
	}

	specs := drafted
	if *contracts != "" {
		entries, err := contractlog.Load(*contracts)
		if err != nil {
			fmt.Fprintf(stderr, "mutate: %s\n", terminalSafe(err.Error()))
			return exitUsage
		}
		if len(entries) == 0 {
			fmt.Fprintf(stderr, "mutate: no recorded contracts in %s; run the tests with %s=%s first\n", terminalSafe(*contracts), contractlog.EnvDir, terminalSafe(*contracts))
			return exitUsage
		}
		specs = specs[:0:0]
		for _, e := range entries {
			specs = append(specs, mutate.Spec{
				AssertionID: e.Assertion.AssertionID, Type: e.Assertion.Type, Spec: e.Assertion.Spec,
				TestID: e.TestID, File: e.File, Line: e.Line,
			})
		}
	}

	rep, err := mutate.Run(subjects, specs, mutate.Options{Seed: *seed, MaxMutants: *maxMutants})
	if err != nil {
		fmt.Fprintf(stderr, "mutate: %s\n", terminalSafe(err.Error()))
		return exitUsage
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "mutate: %v\n", err)
			return exitUsage
		}
	} else {
		printMutationReport(stdout, rep)
	}
	if rep.ApplicableSpecs == 0 {
		fmt.Fprintln(stderr, "mutate: no contract applies to any fixture, so there is no score")
		return exitFailure
	}
	return exitOK
}

func printMutationReport(w io.Writer, rep *mutate.Report) {
	killed, survived := rep.Counts()
	none := len(rep.Mutants) - killed - survived
	if rep.Score == nil {
		fmt.Fprintln(w, "mutation score: n/a (no mutant was scored)")
	} else {
		fmt.Fprintf(w, "mutation score: %.0f%% (%d killed, %d survived)\n", *rep.Score*100, killed, survived)
	}
	fmt.Fprintf(w, "fixtures: %s | seed %d | %d mutants", terminalSafe(strings.Join(rep.Fixtures, ", ")), rep.Seed, len(rep.Mutants))
	if none > 0 {
		fmt.Fprintf(w, " (%d without an applicable contract)", none)
	}
	fmt.Fprintf(w, "\ncontracts: %d applicable, %d skipped (not deterministic)\n\n", rep.ApplicableSpecs, rep.SkippedSpecs)

	fmt.Fprintf(w, "%-22s %7s %7s %9s\n", "operator", "mutants", "killed", "survived")
	for _, op := range mutate.Operators {
		var total, k, s int
		for _, m := range rep.Mutants {
			if m.Operator != op {
				continue
			}
			total++
			switch m.Status {
			case mutate.StatusKilled:
				k++
			case mutate.StatusSurvived:
				s++
			}
		}
		if total > 0 {
			fmt.Fprintf(w, "%-22s %7d %7d %9d\n", op, total, k, s)
		}
	}

	if survived == 0 {
		return
	}
	suggested := map[string]mutate.Suggestion{}
	for _, sg := range rep.SurvivorsSuggestions {
		suggested[sg.MutantID] = sg
	}
	fmt.Fprintln(w, "\nsurvivors:")
	for _, m := range rep.Mutants {
		if m.Status != mutate.StatusSurvived {
			continue
		}
		fmt.Fprintf(w, "  %s\n    %s\n", terminalSafe(m.ID), terminalSafe(m.Description))
		if sg, ok := suggested[m.ID]; ok {
			fmt.Fprintf(w, "    kill with: %s %s\n    (%s)\n", sg.Contract.Type, terminalSafe(string(sg.Contract.Spec)), terminalSafe(sg.Contract.Description))
		} else {
			fmt.Fprintln(w, "    no suggested contract")
		}
	}
}
