package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/attest-ai/attest/engine/internal/report"
)

func init() {
	register(command{
		name:    "view",
		summary: "write a self-contained offline HTML failure viewer: <report.json...> [--baseline <report.json>...] [--mutation <mutate.json>] [--out F] [--no-open] [--json]",
		run: func(args []string) {
			os.Exit(runView(args, os.Stdout, os.Stderr, defaultViewEnv()))
		},
		schema: "cli/view.schema.json",
		exits: []exitCode{
			{exitOK, "success: the page was written"},
			{exitUsage, "usage error, or unreadable or invalid input, or the page could not be written"},
		},
	})
}

const viewUsage = `usage: attest-engine view <report.json...> [--baseline <report.json>...] [--mutation <mutate.json>] [--out <file>] [--no-open] [--json]

Writes one self-contained HTML page (no scripts, no external resources) with the
test list, the trace tree with the failing span highlighted, expected versus
actual, replay or live provenance, cost, a baseline-versus-head diff and
surviving mutants. Several reports merge. The default output is
attest-report.html next to the first report. The page opens in the default
browser only when stdout is a terminal, CI is unset and --no-open is absent.
Traces appear when the run set ATTEST_REPORT_TRACES=1.`

const defaultViewOut = "attest-report.html"

// viewEnv is what the command reads from its surroundings, injectable for tests.
type viewEnv struct {
	isTTY bool
	ci    bool
	// open launches the OS opener for an absolute file path.
	open func(path string) error
}

func defaultViewEnv() viewEnv {
	info, err := os.Stdout.Stat()
	return viewEnv{
		isTTY: err == nil && info.Mode()&os.ModeCharDevice != 0,
		ci:    os.Getenv("CI") != "",
		open:  openInBrowser,
	}
}

// openInBrowser starts the platform's file opener with path as one argv
// element; no shell is involved.
func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

type viewResult struct {
	Out      string `json:"out"`
	Reports  int    `json:"reports"`
	Tests    int    `json:"tests"`
	Failures int    `json:"failures"`
	Traces   int    `json:"traces"`
	Baseline bool   `json:"baseline"`
	Mutation bool   `json:"mutation"`
	Opened   bool   `json:"opened"`
}

// viewArgs are the parsed command line.
type viewArgs struct {
	reports  []string
	baseline stringList
	mutation string
	out      string
	noOpen   bool
	asJSON   bool
}

// parseViewArgs accepts flags and report paths in any order.
func parseViewArgs(args []string, stderr io.Writer) (*viewArgs, bool) {
	va := &viewArgs{}
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&va.baseline, "baseline", "baseline report-v2 file; repeat to merge several")
	fs.StringVar(&va.mutation, "mutation", "", "`attest mutate --json` output to list surviving mutants from")
	fs.StringVar(&va.out, "out", "", "output file (default attest-report.html next to the first report)")
	fs.BoolVar(&va.noOpen, "no-open", false, "write the page without opening a browser")
	fs.BoolVar(&va.asJSON, "json", false, `print {"out","reports","tests","failures","traces","baseline","mutation","opened"} as JSON`)
	for {
		if err := fs.Parse(args); err != nil {
			return nil, false
		}
		if fs.NArg() == 0 {
			break
		}
		va.reports = append(va.reports, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(va.reports) == 0 {
		fmt.Fprintln(stderr, "view: at least one report is required")
		fmt.Fprintln(stderr, viewUsage)
		return nil, false
	}
	if va.out == "" {
		va.out = filepath.Join(filepath.Dir(va.reports[0]), defaultViewOut)
	}
	return va, true
}

// runView writes the page and, on an interactive terminal outside CI, opens it.
func runView(args []string, stdout, stderr io.Writer, env viewEnv) int {
	va, ok := parseViewArgs(args, stderr)
	if !ok {
		return exitUsage
	}
	if clash := inputNamed(va); clash != "" {
		fmt.Fprintf(stderr, "view: --out %s would overwrite an input file\n", clash)
		return exitUsage
	}
	v, err := report.BuildView(report.ViewOptions{Reports: va.reports, Baselines: va.baseline, Mutation: va.mutation})
	if err != nil {
		fmt.Fprintf(stderr, "view: %v\n", err)
		return exitUsage
	}
	var page bytes.Buffer
	if err := report.RenderHTML(&page, v); err != nil {
		fmt.Fprintf(stderr, "view: %v\n", err)
		return exitUsage
	}
	if err := writeFileAtomic(va.out, page.Bytes()); err != nil {
		fmt.Fprintf(stderr, "view: write %s: %v\n", va.out, err)
		return exitUsage
	}
	opened := false
	if env.isTTY && !env.ci && !va.noOpen {
		opened = tryOpen(va.out, stderr, env)
	}
	res := viewResult{
		Out: va.out, Reports: len(va.reports), Tests: len(v.Tests), Failures: v.Failures, Traces: v.TraceCount,
		Baseline: v.Baseline != nil, Mutation: v.Mutation != nil, Opened: opened,
	}
	if va.asJSON {
		if err := json.NewEncoder(stdout).Encode(res); err != nil {
			fmt.Fprintf(stderr, "view: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "wrote %s (%d tests, %d failed, %d traces)\n", va.out, res.Tests, res.Failures, res.Traces)
	return exitOK
}

// inputNamed returns --out when it is also an input path.
func inputNamed(va *viewArgs) string {
	out, err := filepath.Abs(va.out)
	if err != nil {
		return ""
	}
	inputs := append(append([]string{va.mutation}, va.reports...), va.baseline...)
	for _, in := range inputs {
		if in == "" {
			continue
		}
		if abs, err := filepath.Abs(in); err == nil && abs == out {
			return va.out
		}
	}
	return ""
}

// tryOpen runs the opener; a failure prints where the page is and does not
// fail the command.
func tryOpen(path string, stderr io.Writer, env viewEnv) bool {
	abs, err := filepath.Abs(path)
	if err == nil {
		err = env.open(abs)
	}
	if err != nil {
		fmt.Fprintf(stderr, "view: could not open a browser (%v); open %s\n", err, path)
		return false
	}
	return true
}

// writeFileAtomic writes data to path with mode 0644 through a temporary file
// in the same directory and a rename.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".attest-view-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
