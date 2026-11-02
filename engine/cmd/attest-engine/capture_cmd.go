package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/attest-ai/attest/engine/internal/capture"
	"github.com/attest-ai/attest/engine/internal/redact"
	"github.com/attest-ai/attest/engine/internal/trace"
)

func init() {
	register(command{name: "capture", summary: "record a run as a verified fixture (trace, model exchanges, drafted contracts)", run: handleCaptureCommand})
}

const captureUsage = `usage: attest-engine capture --name <name> [options] -- <command> [args...]
       attest-engine capture --name <name> --from <otlp.json> [options]
       attest-engine capture --name <name> --serve [options]
       attest-engine capture --check-redaction <fixture.attest.json>

Runs <command> with OPENAI_BASE_URL and ANTHROPIC_BASE_URL pointing at a
recording proxy and OTEL_EXPORTER_OTLP_TRACES_ENDPOINT at a trace receiver,
both on 127.0.0.1. Writes <out-dir>/<name>.attest.json with secrets redacted,
volatile values templated and drafted contracts verified. Exits 0 when the
fixture is verified, 1 when it is not.

options:
  --out-dir <dir>        fixture directory (default tests/attest/fixtures)
  --expect-fail          save an incident reproduction: verified when drafted
                         contracts fail, recorded as expected failures
  --agent-id <id>        trace agent_id (default: the first turn's agent name)
  --result-file <path>   also write {"fixture", "verified"} JSON to <path>
  --from <otlp.json>     build from an OTLP/JSON export (trace only; no replay)
  --serve                in-process capture: print the environment as one JSON
                         line, then finalize when stdin sends "finalize"`

func handleCaptureCommand(args []string) {
	os.Exit(runCapture(args, os.Stdin, os.Stdout, os.Stderr))
}

type captureOptions struct {
	name           string
	outDir         string
	expectFail     bool
	agentID        string
	resultFile     string
	from           string
	serve          bool
	checkRedaction string
	command        []string
}

func parseCaptureArgs(args []string, stderr io.Writer) (*captureOptions, bool) {
	o := &captureOptions{}
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.name, "name", "", "")
	fs.StringVar(&o.outDir, "out-dir", filepath.Join("tests", "attest", "fixtures"), "")
	fs.BoolVar(&o.expectFail, "expect-fail", false, "")
	fs.StringVar(&o.agentID, "agent-id", "", "")
	fs.StringVar(&o.resultFile, "result-file", "", "")
	fs.StringVar(&o.from, "from", "", "")
	fs.BoolVar(&o.serve, "serve", false, "")
	fs.StringVar(&o.checkRedaction, "check-redaction", "", "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "capture: %s\n%s\n", terminalSafe(err.Error()), captureUsage)
		return nil, false
	}
	o.command = fs.Args()
	usage := func(msg string) (*captureOptions, bool) {
		fmt.Fprintf(stderr, "capture: %s\n%s\n", msg, captureUsage)
		return nil, false
	}
	if o.checkRedaction != "" {
		if o.name != "" || o.from != "" || o.serve || len(o.command) > 0 {
			return usage("--check-redaction takes only a fixture path")
		}
		return o, true
	}
	if err := capture.ValidateName(o.name); err != nil {
		return usage(terminalSafe(err.Error()))
	}
	modes := 0
	for _, on := range []bool{o.from != "", o.serve, len(o.command) > 0} {
		if on {
			modes++
		}
	}
	if modes != 1 {
		return usage("give exactly one of -- <command>, --from <otlp.json> or --serve")
	}
	return o, true
}

func runCapture(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := parseCaptureArgs(args, stderr)
	if !ok {
		return 2
	}
	if o.checkRedaction != "" {
		return runCheckRedaction(o.checkRedaction, stdout, stderr)
	}
	environ := os.Environ()
	in := capture.BuildInput{
		Name:       o.name,
		ExpectFail: o.expectFail,
		AgentID:    o.agentID,
		Redactor:   redact.FromEnviron(environ),
	}
	var err error
	switch {
	case o.from != "":
		err = captureFromFile(o.from, &in)
	case o.serve:
		err = captureServe(environ, &in, stdin, stdout)
	default:
		err = captureCommand(o.command, environ, &in, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(in.Redactor.String(err.Error())))
		return 1
	}
	return finishCapture(o, in, stdout, stderr)
}

func captureFromFile(path string, in *capture.BuildInput) error {
	data, err := readOTLPInput(path)
	if err != nil {
		return err
	}
	spans, err := trace.ParseOTLP(data)
	if err != nil {
		return err
	}
	in.Spans = spans
	in.Source = capture.Source{Kind: capture.SourceOTLPFile}
	return nil
}

// captureCommand runs the command under capture. The command's exit code is
// recorded; a failing command still produces a fixture.
func captureCommand(argv []string, environ []string, in *capture.BuildInput, stderr io.Writer) error {
	s, err := capture.Start(capture.UpstreamsFromEnv(environ))
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = withEnv(environ, s.Env())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		s.Close()
		return fmt.Errorf("running %s: %w", argv[0], err)
	}
	// Interrupts and terminations go to the child, which decides how to
	// stop; the capture finishes after it exits. A terminal interrupt
	// reaches the child directly as well, which is harmless.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	stopForward := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = cmd.Process.Signal(sig)
			case <-stopForward:
				return
			}
		}
	}()
	runErr := cmd.Wait()
	signal.Stop(signals)
	close(stopForward)
	closeErr := s.Close()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
		fmt.Fprintf(stderr, "capture: command exited %d; capturing what it produced\n", code)
	case runErr != nil:
		return fmt.Errorf("running %s: %w", argv[0], runErr)
	}
	if closeErr != nil {
		return closeErr
	}
	spans, exchanges, incomplete := s.Recorded()
	if err := requireComplete(incomplete); err != nil {
		return err
	}
	in.Spans, in.Exchanges = spans, exchanges
	in.Source = capture.Source{Kind: capture.SourceCommand, ReplayCapable: true, Command: argv, ExitCode: &code}
	return nil
}

// captureServe hosts the listeners for an in-process helper: it prints the
// environment as one JSON line, then waits for "finalize" on stdin. End of
// input without "finalize" aborts without writing a fixture.
func captureServe(environ []string, in *capture.BuildInput, stdin io.Reader, stdout io.Writer) error {
	s, err := capture.Start(capture.UpstreamsFromEnv(environ))
	if err != nil {
		return err
	}
	if err := writeJSONLine(stdout, map[string]any{"env": s.Env()}); err != nil {
		s.Close()
		return err
	}
	finalize := false
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "finalize" {
			finalize = true
			break
		}
	}
	if err := s.Close(); err != nil {
		return err
	}
	if !finalize {
		return errors.New("aborted: input closed before finalize; no fixture written")
	}
	spans, exchanges, incomplete := s.Recorded()
	if err := requireComplete(incomplete); err != nil {
		return err
	}
	in.Spans, in.Exchanges = spans, exchanges
	in.Source = capture.Source{Kind: capture.SourceInProcess, ReplayCapable: true}
	return nil
}

// requireComplete refuses a capture with model calls whose response the
// agent never finished reading (or cancelled): a fixture without them could
// not replay the run.
func requireComplete(incomplete int) error {
	if incomplete == 0 {
		return nil
	}
	return fmt.Errorf("%d model %s never completed (the client cancelled or did not read to the end); no fixture written",
		incomplete, plural(incomplete, "call", "calls"))
}

func finishCapture(o *captureOptions, in capture.BuildInput, stdout, stderr io.Writer) int {
	f, err := capture.BuildFixture(in)
	if err != nil {
		fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(in.Redactor.String(err.Error())))
		return 1
	}
	data, err := capture.Marshal(f)
	if err != nil {
		fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
		return 1
	}
	path := filepath.Join(o.outDir, capture.FixtureFile(o.name))
	if err := capture.WriteFile(path, data); err != nil {
		fmt.Fprintf(stderr, "capture: writing fixture: %s\n", terminalSafe(err.Error()))
		return 1
	}
	result := map[string]any{"fixture": path, "verified": f.Verification.Verified}
	if o.resultFile != "" {
		if err := writeJSONFile(o.resultFile, result); err != nil {
			fmt.Fprintf(stderr, "capture: writing result file: %s\n", terminalSafe(err.Error()))
			return 1
		}
	}
	summary := stderr
	if o.serve {
		if err := writeJSONLine(stdout, result); err != nil {
			fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
			return 1
		}
	} else if o.from != "" {
		summary = stdout
	}
	writeCaptureSummary(summary, path, f)
	if !f.Verification.Verified {
		return 1
	}
	return 0
}

func writeCaptureSummary(w io.Writer, path string, f *capture.Fixture) {
	fmt.Fprintf(w, "capture %s: %s\n", f.Name, terminalSafe(path))
	kinds := map[string]int{}
	for _, s := range f.Trace.Steps {
		kinds[s.Type]++
	}
	tc := "OK"
	if !f.Verification.TraceCheck.OK {
		tc = fmt.Sprintf("%d %s", f.Verification.TraceCheck.Errors, plural(f.Verification.TraceCheck.Errors, "error", "errors"))
	}
	fmt.Fprintf(w, "  trace: %s; trace check %s\n", stepSummary(kinds), tc)
	if f.Source.ReplayCapable {
		apis := map[string]int{}
		for _, ex := range f.Exchanges {
			apis[ex.Provider+" "+ex.API]++
		}
		fmt.Fprintf(w, "  exchanges: %d recorded%s\n", len(f.Exchanges), apiSummary(apis))
		if len(f.Exchanges) == 0 {
			fmt.Fprintln(w, "  no model calls reached the proxy: does the agent read OPENAI_BASE_URL or ANTHROPIC_BASE_URL?")
		}
	} else {
		fmt.Fprintln(w, "  exchanges: none (trace-only fixture; not replay-capable)")
	}
	fmt.Fprintln(w, "  contracts:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range f.Verification.Results {
		fmt.Fprintf(tw, "    %s\t%s\t%s\n", r.Status, r.Contract, terminalSafe(r.Explanation))
	}
	tw.Flush()
	v := f.Verification
	switch {
	case v.Verified && v.ExpectFail:
		fmt.Fprintf(w, "  verified: incident reproduction; expected %s: %s\n", plural(len(v.ExpectedFailures), "failure", "failures"), strings.Join(v.ExpectedFailures, ", "))
	case v.Verified:
		fmt.Fprintln(w, "  verified: every drafted contract passes")
	case v.ExpectFail && len(v.ExpectedFailures) == 0:
		fmt.Fprintln(w, "  not verified: --expect-fail given but every drafted contract passes")
	default:
		fmt.Fprintf(w, "  not verified: %s\n", unverifiedReason(f))
	}
}

func unverifiedReason(f *capture.Fixture) string {
	if !f.Verification.TraceCheck.OK {
		return "the captured trace fails trace check"
	}
	var failed []string
	for _, r := range f.Verification.Results {
		if r.Status != "pass" && !contains(f.Verification.ExpectedFailures, r.Contract) {
			failed = append(failed, r.Contract)
		}
	}
	msg := "failing " + strings.Join(failed, ", ")
	if !f.Verification.ExpectFail {
		msg += "; edit the drafted contracts or re-run with --expect-fail to save an incident reproduction"
	}
	return msg
}

func apiSummary(apis map[string]int) string {
	if len(apis) == 0 {
		return ""
	}
	keys := make([]string, 0, len(apis))
	for k := range apis {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s ×%d", k, apis[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// withEnv returns environ with the given variables set, replacing any
// existing values.
func withEnv(environ []string, set map[string]string) []string {
	out := make([]string, 0, len(environ)+len(set))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[name]; !replaced {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+set[k])
	}
	return out
}

// writeJSONLine writes v as one line of compact JSON.
func writeJSONLine(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func writeJSONFile(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeJSONLine(f, v); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// runCheckRedaction scans a fixture for secrets: values of environment
// variables whose names end in KEY, TOKEN, SECRET or PASSWORD, and
// key-shaped strings. It prints where each hit is, never the secret.
func runCheckRedaction(path string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "capture --check-redaction: %s\n", terminalSafe(err.Error()))
		return 1
	}
	hits, err := capture.CheckRedaction(data, redact.FromProcessEnv())
	if err != nil {
		fmt.Fprintf(stderr, "capture --check-redaction: %s: %s\n", terminalSafe(path), terminalSafe(err.Error()))
		return 1
	}
	if len(hits) == 0 {
		fmt.Fprintf(stdout, "check-redaction %s: clean\n", terminalSafe(path))
		return 0
	}
	fmt.Fprintf(stdout, "check-redaction %s: %d %s\n", terminalSafe(path), len(hits), plural(len(hits), "secret", "secrets"))
	for _, h := range hits {
		fmt.Fprintf(stdout, "  %s\t%s\n", h.Kind, terminalSafe(h.Path))
	}
	return 1
}
