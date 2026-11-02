package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/attest-ai/attest/engine/internal/trace"
)

func init() {
	register(command{name: "trace", summary: "check or convert an OTLP/JSON trace export (check|convert)", run: handleTraceCommand})
}

const traceUsage = `usage: attest-engine trace check [--json] <otlp.json|->
       attest-engine trace convert [--agent-id <id>] <otlp.json|->`

func handleTraceCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, traceUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "check":
		os.Exit(runTraceCheck(args[1:], os.Stdout, os.Stderr))
	case "convert":
		os.Exit(runTraceConvert(args[1:], os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "unknown trace subcommand: %s\n%s\n", args[0], traceUsage)
		os.Exit(2)
	}
}

// readOTLPInput reads a path, or stdin for "-", refusing input above the
// OTLP size limit.
func readOTLPInput(path string) ([]byte, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, trace.MaxOTLPBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > trace.MaxOTLPBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", trace.MaxOTLPBytes)
	}
	return data, nil
}

// singleInput parses flags and returns the one positional input path.
func singleInput(fs *flag.FlagSet, args []string, stderr io.Writer) (string, bool) {
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, traceUsage)
		return "", false
	}
	return fs.Arg(0), true
}

func runTraceConvert(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trace convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentID := fs.String("agent-id", "", "agent_id for the trace (default: the first turn's gen_ai.agent.name)")
	path, ok := singleInput(fs, args, stderr)
	if !ok {
		return 2
	}
	data, err := readOTLPInput(path)
	if err != nil {
		fmt.Fprintf(stderr, "trace convert: %s\n", terminalSafe(err.Error()))
		return 1
	}
	t, err := trace.ConvertOTLP(data, trace.ConvertOptions{AgentID: *agentID})
	if err != nil {
		fmt.Fprintf(stderr, "trace convert: %s\n", terminalSafe(err.Error()))
		return 1
	}
	if err := writeJSON(stdout, t); err != nil {
		fmt.Fprintf(stderr, "trace convert: %s\n", terminalSafe(err.Error()))
		return 1
	}
	return 0
}

func runTraceCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trace check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	path, ok := singleInput(fs, args, stderr)
	if !ok {
		return 2
	}
	data, err := readOTLPInput(path)
	if err != nil {
		fmt.Fprintf(stderr, "trace check: %s\n", terminalSafe(err.Error()))
		return 1
	}
	spans, err := trace.ParseOTLP(data)
	if err != nil {
		fmt.Fprintf(stderr, "trace check: %s\n", terminalSafe(err.Error()))
		return 1
	}
	report := trace.Check(spans)
	exit := 0
	if !report.OK {
		exit = 1
	}
	if *asJSON {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "trace check: %s\n", terminalSafe(err.Error()))
			return 1
		}
		return exit
	}
	writeCheckText(stdout, path, report)
	return exit
}

func writeCheckText(w io.Writer, path string, r *trace.CheckReport) {
	fmt.Fprintf(w, "trace check %s\n", terminalSafe(path))
	fmt.Fprintf(w, "trace %s: %d spans, attest trace schema v%d\n", r.TraceID, r.Spans, r.SchemaVersion)
	fmt.Fprintf(w, "steps: %s; %s, %s, %s\n", stepSummary(r.Steps),
		count(r.Turns, "turn", "turns"), count(r.Handoffs, "handoff", "handoffs"), count(r.Retries, "retry", "retries"))

	errs := r.Errors()
	if len(errs) == 0 {
		fmt.Fprintln(w, "structure: OK")
	} else {
		fmt.Fprintf(w, "structure: %d %s\n", len(errs), plural(len(errs), "error", "errors"))
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, is := range r.Issues {
		span := is.SpanID
		if span == "" {
			span = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\tspan %s\t%s\n", is.Severity, is.Code, span, terminalSafe(is.Message))
	}
	tw.Flush()

	if len(r.Coverage) == 0 {
		fmt.Fprintln(w, "coverage: complete")
		return
	}
	fmt.Fprintln(w, "coverage: missing evidence")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, g := range r.Coverage {
		fmt.Fprintf(tw, "  %s\t%d of %d %s spans lack %s\n", g.Field, g.Missing, g.Total, g.SpanKind, strings.Join(g.Attributes, " / "))
	}
	tw.Flush()
}

func stepSummary(steps map[string]int) string {
	if len(steps) == 0 {
		return "none"
	}
	kinds := make([]string, 0, len(steps))
	for k := range steps {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%d %s", steps[k], k)
	}
	return strings.Join(parts, ", ")
}

func count(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, many))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// terminalSafe escapes control and format characters (ANSI escapes, newlines,
// bidi overrides) in text that comes from the checked file, so a crafted
// export cannot rewrite the terminal or forge report lines.
func terminalSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
