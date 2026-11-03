package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/attest-ai/attest/engine/internal/report"
)

func init() {
	register(command{
		name:    "report",
		summary: "render report-v2 files as JUnit XML, a PR summary or annotation rows (junit|summary|rows)",
		run:     handleReportCommand,
		subs: []subcommand{
			{name: "junit", schema: "cli/report-junit.schema.json"},
			{name: "summary", schema: "cli/report-summary.schema.json"},
			{name: "rows", schema: "cli/report-rows.schema.json"},
		},
		exits: []exitCode{
			{exitOK, "success"},
			{exitUsage, "usage error, or unreadable or invalid input, or the output could not be written"},
		},
	})
}

const reportUsage = `usage: attest-engine report <junit|summary|rows> --report <path> [--report <path>...] [options]

  junit    --out <file> [--json]           write JUnit XML; --json prints {"out", "tests", ...}
  summary  [--policy-result <file>] [--json]
                                           print the PR summary as Markdown, or {"markdown"}
  rows     --json                          print failure and replay-miss rows as JSON

Several --report files merge. Exit 2 for unreadable or invalid input.`

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func handleReportCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, reportUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "junit":
		os.Exit(reportJUnit(args[1:], os.Stdout, os.Stderr))
	case "summary":
		os.Exit(reportSummary(args[1:], os.Stdout, os.Stderr))
	case "rows":
		os.Exit(reportRows(args[1:], os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "unknown report subcommand: %s\n%s\n", args[0], reportUsage)
		os.Exit(2)
	}
}

func newReportFlags(name string, stderr io.Writer) (*flag.FlagSet, *stringList) {
	fs := flag.NewFlagSet("report "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var reports stringList
	fs.Var(&reports, "report", "report-v2 file; repeat to merge several")
	return fs, &reports
}

func loadReports(reports stringList, stderr io.Writer, name string) (*report.CIReport, bool) {
	merged, err := report.LoadMergedCI(reports)
	if err != nil {
		fmt.Fprintf(stderr, "report %s: %v\n", name, err)
		return nil, false
	}
	return merged, true
}

func reportJUnit(args []string, stdout, stderr io.Writer) int {
	fs, reports := newReportFlags("junit", stderr)
	out := fs.String("out", "", "JUnit XML output file")
	asJSON := fs.Bool("json", false, `print {"out","tests","failures","skipped"} as JSON`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "report junit: --out is required")
		return 2
	}
	merged, ok := loadReports(*reports, stderr, "junit")
	if !ok {
		return 2
	}
	xmlDoc, err := report.GenerateJUnitXML(merged)
	if err != nil {
		fmt.Fprintf(stderr, "report junit: %v\n", err)
		return 2
	}
	if err := os.WriteFile(*out, append(xmlDoc, '\n'), 0o644); err != nil {
		fmt.Fprintf(stderr, "report junit: write %s: %v\n", *out, err)
		return 2
	}
	if *asJSON {
		return writeReportJSON(stdout, stderr, "junit", junitResult(*out, merged))
	}
	return 0
}

// junitResult is the `report junit --json` document: the file written and
// the testcase counts it holds.
func junitResult(out string, r *report.CIReport) map[string]any {
	failures, skipped := 0, 0
	for _, t := range r.Tests {
		switch t.Outcome {
		case report.OutcomeFailed:
			failures++
		case report.OutcomeSkipped:
			skipped++
		}
	}
	return map[string]any{"out": out, "tests": len(r.Tests), "failures": failures, "skipped": skipped}
}

// writeReportJSON writes v as the subcommand's one JSON document.
func writeReportJSON(stdout, stderr io.Writer, name string, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintf(stderr, "report %s: %v\n", name, err)
		return 2
	}
	return 0
}

func reportSummary(args []string, stdout, stderr io.Writer) int {
	fs, reports := newReportFlags("summary", stderr)
	policyPath := fs.String("policy-result", "", "policy evaluate output (JSON) to show as the verdict")
	asJSON := fs.Bool("json", false, `print {"markdown"} as JSON`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	merged, ok := loadReports(*reports, stderr, "summary")
	if !ok {
		return 2
	}
	var outcome *report.PolicyOutcome
	if *policyPath != "" {
		var err error
		if outcome, err = report.LoadPolicyOutcome(*policyPath); err != nil {
			fmt.Fprintf(stderr, "report summary: %v\n", err)
			return 2
		}
	}
	markdown := report.RenderSummary(merged, outcome)
	if *asJSON {
		return writeReportJSON(stdout, stderr, "summary", map[string]any{"markdown": markdown})
	}
	if _, err := io.WriteString(stdout, markdown); err != nil {
		fmt.Fprintf(stderr, "report summary: %v\n", err)
		return 2
	}
	return 0
}

func reportRows(args []string, stdout, stderr io.Writer) int {
	fs, reports := newReportFlags("rows", stderr)
	asJSON := fs.Bool("json", false, "print rows as one JSON array")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*asJSON {
		fmt.Fprintln(stderr, "report rows: --json is required")
		return 2
	}
	merged, ok := loadReports(*reports, stderr, "rows")
	if !ok {
		return 2
	}
	rows := report.FindingRows(merged)
	if rows == nil {
		rows = []report.FindingRow{}
	}
	return writeReportJSON(stdout, stderr, "rows", rows)
}
