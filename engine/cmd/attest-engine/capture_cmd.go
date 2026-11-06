package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/mattn/go-isatty"

	"github.com/attest-framework/attest/engine/internal/capture"
	"github.com/attest-framework/attest/engine/internal/contractdsl"
	"github.com/attest-framework/attest/engine/internal/pathguard"
	"github.com/attest-framework/attest/engine/internal/redact"
	"github.com/attest-framework/attest/engine/internal/trace"
	"github.com/attest-framework/attest/engine/internal/world"
)

func init() {
	register(command{
		name:    "capture",
		summary: "record a run as a fixture consistent with its recording (trace, model exchanges, drafted contracts)",
		run:     handleCaptureCommand,
		schema:  "cli/capture.schema.json",
		exits: []exitCode{
			{exitOK, "the fixture is verified or captured with nothing to verify, or --check-redaction found no secrets; the command's own exit code is recorded, not propagated"},
			{exitFailure, "the fixture is not verified (a drafted contract failed, the trace check failed, no step was mapped, --expect-fail drafted nothing, or nothing was drafted while the command exited non-zero or recorded no model call) or was not written, the capture failed, or --check-redaction found secrets"},
			{exitUsage, "usage error, an invalid --world file, or a --refresh that was not confirmed"},
		},
	})
}

const captureUsage = `usage: attest-engine capture --name <name> [options] -- <command> [args...]
       attest-engine capture --name <name> --from <otlp.json> [options]
       attest-engine capture --name <name> --serve [options]
       attest-engine capture --refresh <name> [options]
       attest-engine capture --check-redaction <fixture.attest.json>

Runs <command> with OPENAI_BASE_URL and ANTHROPIC_BASE_URL pointing at a
recording proxy and OTEL_EXPORTER_OTLP_TRACES_ENDPOINT at a trace receiver,
both on 127.0.0.1. Writes <out-dir>/<name>.attest.json with secrets redacted,
volatile values templated and drafted contracts checked against the run.
Exits 0 when the fixture is consistent with the recording or the run was
healthy with nothing to verify (outcome nothing_to_verify), 1 when it is not
verified.

options:
  --out-dir <dir>        fixture directory (default tests/attest/fixtures)
  --expect-fail          save an incident reproduction: consistent when drafted
                         contracts fail, recorded as expected failures
  --agent-id <id>        trace agent_id (default: the first turn's agent name;
                         with --refresh, the fixture's)
  --result-file <path>   also write {"fixture", "verified", "outcome"} JSON to <path>
  --from <otlp.json>     build from an OTLP/JSON export (trace only; no replay)
  --yes                  with --refresh: run the recorded command without asking
                         (required when CI is set, with --json, or when stdin
                         is not a terminal)
  --json                 print one JSON result object to stdout; the
                         command's stdout and the summary go to stderr (not
                         with --serve)
  --refresh <name>       re-run the fixture's recorded command with its
                         original flags (--expect-fail included) and overwrite
                         it, then print what changed per exchange (system
                         prompt, tool schemas, messages, tool results,
                         model, params; --json adds a "diff" object);
                         a fixture with a world is re-run in that world,
                         hosted again from the --world file the fixture
                         records (repo-relative); refuses when its
                         definition_sha256 changed or the file is missing;
                         refuses when the command's arguments were
                         redacted or templated, or the fixture has no
                         command (--from, in-process)
  --world <world.yaml>   also host the world the agent acts on (its base URL
                         variable is set for the command) and store the
                         world's final state in the fixture's world section
  --serve                in-process capture: print the environment as one JSON
                         line, then finalize when stdin sends "finalize" or
                         {"finalize":true,"environ":[...]} (secrets in that
                         environment are redacted too)`

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
	refresh        string
	oldFixture     *capture.Fixture // with --refresh: the fixture before the re-run
	world          string
	refreshWorld   *world.World // with --refresh: the world resolveRefreshWorld verified; hosted as is, never reloaded
	worldFile      string       // the --world file as the fixture records it: repo-relative, slash separated; empty when outside the repo root
	yes            bool
	json           bool
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
	fs.StringVar(&o.refresh, "refresh", "", "")
	fs.StringVar(&o.world, "world", "", "")
	fs.BoolVar(&o.yes, "yes", false, "")
	fs.BoolVar(&o.json, "json", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "capture: %s\n%s\n", terminalSafe(err.Error()), captureUsage)
		return nil, false
	}
	o.command = fs.Args()
	usage := func(msg string) (*captureOptions, bool) {
		fmt.Fprintf(stderr, "capture: %s\n%s\n", msg, captureUsage)
		return nil, false
	}
	if o.json && o.serve {
		return usage(jsonServeConflict)
	}
	if o.checkRedaction != "" {
		if o.name != "" || o.from != "" || o.serve || o.world != "" || len(o.command) > 0 {
			return usage("--check-redaction takes only a fixture path")
		}
		return o, true
	}
	if o.yes && o.refresh == "" {
		return usage("--yes applies to --refresh only")
	}
	if o.refresh != "" {
		if o.name != "" || o.from != "" || o.serve || o.expectFail || o.world != "" || len(o.command) > 0 {
			return usage("--refresh takes only a fixture name, --out-dir, --agent-id and --result-file")
		}
		if err := capture.ValidateName(o.refresh); err != nil {
			return usage(terminalSafe(err.Error()))
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
	if o.world != "" && o.from != "" {
		return usage("--world needs a run to host it for; --from builds from a finished export")
	}
	return o, true
}

func runCapture(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := parseCaptureArgs(args, stderr)
	if !ok {
		return 2
	}
	if o.checkRedaction != "" {
		return runCheckRedaction(o.checkRedaction, o.json, stdout, stderr)
	}
	if o.refresh != "" {
		if err := resolveRefresh(o); err != nil {
			fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
			return 1
		}
		if code, ok := confirmRefresh(o, stdin, stderr); !ok {
			return code
		}
	}
	environ := os.Environ()
	dotenv, err := cwdDotenvRedactor()
	if err != nil {
		fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
		return 1
	}
	in := capture.BuildInput{
		Name:       o.name,
		ExpectFail: o.expectFail,
		AgentID:    o.agentID,
		Redactor:   redact.FromEnviron(environ).Merge(dotenv),
	}
	var opts []capture.Option
	if o.world != "" {
		w := o.refreshWorld
		if w == nil {
			var werr error
			if w, werr = loadWorld(o.world); werr != nil {
				printWorldError(stderr, "capture", o.world, werr)
				return 2
			}
		}
		opts = append(opts, capture.WithWorld(w))
		if o.refresh == "" {
			if o.worldFile, err = repoRelative(o.outDir, o.world); err != nil {
				fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
				return 1
			}
			if o.worldFile == "" {
				fmt.Fprintf(stderr, "capture: note: %s is outside the repository root, so the fixture cannot record it and --refresh will not be able to host the world again\n", terminalSafe(o.world))
			}
		}
	}
	switch {
	case o.from != "":
		err = captureFromFile(o.from, &in)
	case o.serve:
		err = captureServe(environ, &in, stdin, stdout, opts)
	default:
		childOut := io.Writer(os.Stdout)
		if o.json {
			childOut = os.Stderr
		}
		err = captureCommand(o.command, environ, &in, childOut, stderr, opts)
	}
	if err != nil {
		fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(in.Redactor.String(err.Error())))
		return 1
	}
	if o.world != "" {
		in.Source.World = &capture.SourceWorld{File: o.worldFile}
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

// captureCommand runs the command under capture, its stdout going to
// childOut. The command's exit code is recorded; a failing command still
// produces a fixture.
func captureCommand(argv []string, environ []string, in *capture.BuildInput, childOut, stderr io.Writer, opts []capture.Option) error {
	s, err := capture.Start(capture.UpstreamsFromEnv(environ), opts...)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = withEnv(environ, s.Env())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, childOut, os.Stderr
	// Interrupts and terminations go to the child, which decides how to
	// stop; the capture finishes after it exits. A terminal interrupt
	// reaches the child directly as well, which is harmless.
	stopForward, err := startForwardingSignals(cmd)
	if err != nil {
		s.Close()
		return fmt.Errorf("running %s: %w", argv[0], err)
	}
	runErr := cmd.Wait()
	stopForward()
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
	spans, exchanges, incomplete, err := s.Recorded()
	if err != nil {
		return err
	}
	if err := requireComplete(incomplete); err != nil {
		return err
	}
	in.Spans, in.Exchanges = spans, exchanges
	in.World = s.WorldSnapshot()
	in.Source = capture.Source{Kind: capture.SourceCommand, ReplayCapable: true, Command: argv, ExitCode: &code}
	return nil
}

// captureServe hosts the listeners for an in-process helper: it prints the
// environment as one JSON line, then waits for "finalize" on stdin. End of
// input without "finalize" aborts without writing a fixture.
func captureServe(environ []string, in *capture.BuildInput, stdin io.Reader, stdout io.Writer, opts []capture.Option) error {
	s, err := capture.Start(capture.UpstreamsFromEnv(environ), opts...)
	if err != nil {
		return err
	}
	if err := writeJSONLine(stdout, map[string]any{"env": s.Env()}); err != nil {
		s.Close()
		return err
	}
	finalize, environAtEnd, err := readFinalize(stdin)
	if cerr := s.Close(); cerr != nil {
		return cerr
	}
	if err != nil {
		return err
	}
	if !finalize {
		return errors.New("aborted: input closed before finalize; no fixture written")
	}
	// Secrets the block loaded into its own environment (a dotenv file, for
	// example) are redacted too.
	in.Redactor = in.Redactor.Merge(redact.FromEnviron(environAtEnd))
	spans, exchanges, incomplete, err := s.Recorded()
	if err != nil {
		return err
	}
	if err := requireComplete(incomplete); err != nil {
		return err
	}
	in.Spans, in.Exchanges = spans, exchanges
	in.World = s.WorldSnapshot()
	in.Source = capture.Source{Kind: capture.SourceInProcess, ReplayCapable: true}
	return nil
}

// maxFinalizeLine bounds the finalize line, which carries the helper's
// environment.
const maxFinalizeLine = 4 << 20

// readFinalize reads stdin until a finalize line: the word finalize, or
// {"finalize": true, "environ": ["NAME=value", ...]} with the helper's
// environment at the end of the block. It reports false when input ends
// first.
func readFinalize(stdin io.Reader) (bool, []string, error) {
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 64<<10), maxFinalizeLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "finalize" {
			return true, nil, nil
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var msg struct {
			Finalize bool     `json:"finalize"`
			Environ  []string `json:"environ"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return false, nil, fmt.Errorf("finalize line: %w", err)
		}
		if msg.Finalize {
			return true, msg.Environ, nil
		}
	}
	return false, nil, sc.Err()
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
	f, built, err := capture.BuildFixtureReport(in)
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
	outcome, _ := capture.OutcomeOf(f)
	result := map[string]any{"fixture": path, "verified": f.Verification.Verified, "outcome": outcome}
	if o.resultFile != "" {
		if err := writeJSONFile(o.resultFile, result); err != nil {
			fmt.Fprintf(stderr, "capture: writing result file: %s\n", terminalSafe(err.Error()))
			return 1
		}
	}
	var diff *capture.Diff
	if o.oldFixture != nil {
		if diff, err = refreshDiff(o.oldFixture, data, in.Redactor.String); err != nil {
			fmt.Fprintf(stderr, "capture: comparing with the previous fixture: %s\n", terminalSafe(in.Redactor.String(err.Error())))
			return 1
		}
	}
	summary := stderr
	if o.json {
		writeCaptureSummary(stderr, path, f)
		writeRefreshDiff(stderr, f, diff)
		res := captureJSONResult(path, f)
		if note := relationalCappedNote(built); note != "" {
			res["relational_capped"] = note
		}
		if diff != nil {
			res["diff"] = diff
		}
		if err := writeJSON(stdout, res); err != nil {
			fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
			return 1
		}
		return verifiedExit(f)
	}
	if o.serve {
		if err := writeJSONLine(stdout, result); err != nil {
			fmt.Fprintf(stderr, "capture: %s\n", terminalSafe(err.Error()))
			return 1
		}
	} else if o.from != "" {
		summary = stdout
	}
	writeCaptureSummary(summary, path, f)
	if note := relationalCappedNote(built); note != "" {
		fmt.Fprintf(summary, "  %s\n", note)
	}
	writeRefreshDiff(summary, f, diff)
	return verifiedExit(f)
}

// relationalCappedNote says which drafted relational contracts were cut and
// why; empty when none were.
func relationalCappedNote(r capture.BuildReport) string {
	var notes []string
	if r.RelationalCapped {
		notes = append(notes, fmt.Sprintf("the world relates more resources than one contract run takes; only the first %d relational contracts are drafted", contractdsl.MaxStatements))
	}
	if r.RelationalTrimmed {
		notes = append(notes, fmt.Sprintf("the world snapshot is too large to check every drafted relational contract; only the first %d are kept", r.RelationalKept))
	}
	return strings.Join(notes, "; ")
}

// refreshDiff compares the fixture a refresh replaced with the one it wrote.
// The new fixture is read back from its written bytes, so the diff covers
// what is on disk: redacted and templated. scrub is applied to every string
// the diff carries.
func refreshDiff(old *capture.Fixture, written []byte, scrub func(string) string) (*capture.Diff, error) {
	newF, err := capture.Unmarshal(written)
	if err != nil {
		return nil, err
	}
	return capture.SemanticDiff(old, newF, scrub)
}

// writeRefreshDiff prints the per-exchange diff of a --refresh; a no-op for
// any other capture.
func writeRefreshDiff(w io.Writer, f *capture.Fixture, diff *capture.Diff) {
	if diff == nil {
		return
	}
	var buf bytes.Buffer
	diff.WriteText(&buf, f.Name, len(f.Exchanges))
	for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		fmt.Fprintln(w, terminalSafe(line))
	}
}

// verifiedExit is 1 only for a not_verified capture: a healthy run with
// nothing to draft exits 0 (and never prints "verified").
func verifiedExit(f *capture.Fixture) int {
	if outcome, _ := capture.OutcomeOf(f); outcome == capture.OutcomeNotVerified {
		return 1
	}
	return 0
}

// captureJSONResult is the `capture --json` result: the fixture written,
// whether it verified, and the captured command's own exit code (null when
// the fixture did not come from a command).
func captureJSONResult(path string, f *capture.Fixture) map[string]any {
	outcome, _ := capture.OutcomeOf(f)
	return map[string]any{
		"outcome":        outcome,
		"name":           f.Name,
		"fixture":        path,
		"verified":       f.Verification.Verified,
		"expect_fail":    f.Verification.ExpectFail,
		"source":         f.Source.Kind,
		"replay_capable": f.Source.ReplayCapable,
		"exit_code":      f.Source.ExitCode,
		"exchanges":      len(f.Exchanges),
		"steps":          len(f.Trace.Steps),
	}
}

func writeCaptureSummary(w io.Writer, path string, f *capture.Fixture) {
	fmt.Fprintf(w, "capture %s: %s\n", terminalSafe(f.Name), terminalSafe(path))
	kinds := map[string]int{}
	for _, s := range f.Trace.Steps {
		kinds[s.Type]++
	}
	tc := "OK"
	if !f.Verification.TraceCheck.OK {
		tc = fmt.Sprintf("%d %s", f.Verification.TraceCheck.Errors, plural(f.Verification.TraceCheck.Errors, "error", "errors"))
	}
	fmt.Fprintf(w, "  trace: %s; trace check %s\n", terminalSafe(stepSummary(kinds)), tc)
	if f.Source.ReplayCapable {
		apis := map[string]int{}
		for _, ex := range f.Exchanges {
			apis[ex.Provider+" "+ex.API]++
		}
		fmt.Fprintf(w, "  exchanges: %d recorded%s\n", len(f.Exchanges), terminalSafe(apiSummary(apis)))
		if len(f.Exchanges) == 0 {
			fmt.Fprintln(w, "  no model calls reached the proxy: does the agent read OPENAI_BASE_URL or ANTHROPIC_BASE_URL?")
		}
	} else {
		fmt.Fprintln(w, "  exchanges: none (trace-only fixture; not replay-capable)")
	}
	fmt.Fprintln(w, "  contracts:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range f.Verification.Results {
		fmt.Fprintf(tw, "    %s\t%s\t%s\n", terminalSafe(r.Status), terminalSafe(r.Contract), terminalSafe(r.Explanation))
	}
	tw.Flush()
	v := f.Verification
	outcome, causes := capture.OutcomeOf(f)
	switch {
	case outcome == capture.OutcomeNothingToVerify:
		kind := "replay-only"
		if !f.Source.ReplayCapable {
			kind = "trace-only"
		}
		fmt.Fprintf(w, "  captured: nothing to verify (no tool call, structured output or cost); %s fixture, no test drafted\n", kind)
	case outcome == capture.OutcomeNotVerified:
		fmt.Fprintf(w, "  not verified: %s\n", terminalSafe(strings.Join(causes, "; ")))
	case v.ExpectFail:
		fmt.Fprintf(w, "  consistent with the recording: incident reproduction; expected %s: %s\n", plural(len(v.ExpectedFailures), "failure", "failures"), terminalSafe(strings.Join(v.ExpectedFailures, ", ")))
	default:
		fmt.Fprintln(w, "  consistent with the recording: every drafted contract passes")
	}
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

// maxDotenvBytes bounds the .env file read into the redactor.
const maxDotenvBytes = 1 << 20

// cwdDotenvRedactor returns a Redactor holding the secrets of the .env file in
// the working directory, which the agent under capture usually loads itself.
// A missing file is no secrets; an unreadable one, one that is not a regular
// file (a symlink to a regular file is followed) and one over 1 MiB are
// errors, so a capture never runs with secrets it could not load. The values
// are only redaction targets: they are never written anywhere.
func cwdDotenvRedactor() (*redact.Redactor, error) {
	f, err := capture.OpenRegular(".env")
	if errors.Is(err, fs.ErrNotExist) {
		return redact.New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDotenvBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	if len(data) > maxDotenvBytes {
		return nil, fmt.Errorf("read .env: larger than %d bytes", maxDotenvBytes)
	}
	return redact.FromDotenv(data), nil
}

// runCheckRedaction scans a fixture for secrets: values of environment
// variables whose names end in KEY, TOKEN, SECRET or PASSWORD, and
// key-shaped strings. It prints where each hit is, never the secret.
func runCheckRedaction(path string, asJSON bool, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "capture --check-redaction: %s\n", terminalSafe(err.Error()))
		return 1
	}
	dotenv, err := cwdDotenvRedactor()
	if err != nil {
		fmt.Fprintf(stderr, "capture --check-redaction: %s\n", terminalSafe(err.Error()))
		return 1
	}
	hits, err := capture.CheckRedaction(data, redact.FromProcessEnv().Merge(dotenv))
	if err != nil {
		fmt.Fprintf(stderr, "capture --check-redaction: %s: %s\n", terminalSafe(path), terminalSafe(err.Error()))
		return 1
	}
	if asJSON {
		secrets := make([]map[string]string, len(hits))
		for i, h := range hits {
			secrets[i] = map[string]string{"kind": h.Kind, "path": h.Path}
		}
		if err := writeJSON(stdout, map[string]any{"fixture": path, "clean": len(hits) == 0, "secrets": secrets}); err != nil {
			fmt.Fprintf(stderr, "capture --check-redaction: %s\n", terminalSafe(err.Error()))
			return 1
		}
		if len(hits) > 0 {
			return 1
		}
		return 0
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

// resolveRefresh fills o from the fixture named by --refresh: its command and
// its --expect-fail flag. A command whose arguments were redacted cannot be
// run again, and neither can a fixture that has no command.
func resolveRefresh(o *captureOptions) error {
	path := filepath.Join(o.outDir, capture.FixtureFile(o.refresh))
	f, err := capture.LoadFixture(path)
	if err != nil {
		return fmt.Errorf("--refresh %s: %w", o.refresh, err)
	}
	if f.Source.Kind != capture.SourceCommand || len(f.Source.Command) == 0 {
		return fmt.Errorf("--refresh %s: the fixture was captured from %s, not from a command; re-run the capture that produced it", o.refresh, f.Source.Kind)
	}
	if len(f.World) > 0 && string(f.World) != "null" {
		if err := resolveRefreshWorld(o, f); err != nil {
			return err
		}
	}
	for _, a := range f.Source.Command {
		switch {
		case strings.Contains(a, redact.Marker):
			return fmt.Errorf("--refresh %s: the recorded command has redacted arguments; re-run `attest capture --name %s%s -- <command>` yourself", o.refresh, o.refresh, expectFailFlag(f.Verification.ExpectFail))
		case capture.HasPlaceholder(a):
			return fmt.Errorf("--refresh %s: the recorded command has templated arguments (an id, timestamp or email replaced by a placeholder); re-run `attest capture --name %s%s -- <command>` yourself", o.refresh, o.refresh, expectFailFlag(f.Verification.ExpectFail))
		}
	}
	o.name, o.command, o.expectFail = o.refresh, f.Source.Command, f.Verification.ExpectFail
	o.oldFixture = f
	if o.agentID == "" {
		o.agentID = f.Trace.AgentID
	}
	return nil
}

// resolveRefreshWorld finds the world file a world fixture was captured with
// and checks it still defines the same world, so a refresh never drops the
// world and never hosts a different one.
func resolveRefreshWorld(o *captureOptions, f *capture.Fixture) error {
	redo := func() string {
		return fmt.Sprintf("re-run `attest capture --name %s%s --world <world.yaml> -- <command>` yourself", o.refresh, expectFailFlag(f.Verification.ExpectFail))
	}
	if f.Source.World == nil || f.Source.World.File == "" {
		return fmt.Errorf("--refresh %s: the fixture carries a world section but does not record the --world file it came from (captured before worlds were recorded, or the file lay outside the repository root); %s", o.refresh, redo())
	}
	file := f.Source.World.File
	if !filepath.IsLocal(filepath.FromSlash(file)) {
		return fmt.Errorf("--refresh %s: the recorded world file %q is not inside the repository; %s", o.refresh, file, redo())
	}
	root, err := repoRoot(o.outDir)
	if err != nil {
		return fmt.Errorf("--refresh %s: %w", o.refresh, err)
	}
	// The fixture is untrusted input: read the world through a root that
	// refuses symlinks leaving the repository and anything but a regular
	// file, and parse those bytes, so nothing outside the repo is opened.
	guard, err := pathguard.NewRoot(root)
	if err != nil {
		return fmt.Errorf("--refresh %s: %w", o.refresh, err)
	}
	defer guard.Close()
	// Lstat reads metadata only and never follows the final link.
	if _, err := os.Lstat(filepath.Join(guard.Path(), filepath.FromSlash(file))); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("--refresh %s: the recorded world file %s is missing; restore it or %s", o.refresh, file, redo())
	}
	data, err := guard.ReadFile(file, world.MaxDefinitionBytes)
	if err != nil {
		return fmt.Errorf("--refresh %s: the recorded world file %s is not a regular file inside the repository (a symlink out of it is refused); %s", o.refresh, file, redo())
	}
	def, err := world.Parse(data)
	if err != nil {
		return fmt.Errorf("--refresh %s: the recorded world file %s does not load: %w", o.refresh, file, err)
	}
	w, err := world.New(def)
	if err != nil {
		return fmt.Errorf("--refresh %s: the recorded world file %s does not load: %w", o.refresh, file, err)
	}
	var recorded struct {
		DefinitionSHA256 string `json:"definition_sha256"`
	}
	if err := json.Unmarshal(f.World, &recorded); err != nil {
		return fmt.Errorf("--refresh %s: the fixture's world section: %w", o.refresh, err)
	}
	if now := w.Snapshot().DefinitionSHA256; now != recorded.DefinitionSHA256 {
		return fmt.Errorf("--refresh %s: the world file %s changed since the fixture was recorded (definition_sha256 %s, now %s); %s so the new world is reviewed", o.refresh, file, recorded.DefinitionSHA256, now, redo())
	}
	o.world, o.worldFile, o.refreshWorld = filepath.Join(root, filepath.FromSlash(file)), file, w
	return nil
}

// repoRoot is the directory a fixture's world file path is relative to: the
// nearest ancestor of the fixtures directory holding a .git entry, else the
// working directory.
func repoRoot(outDir string) (string, error) {
	dir, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return os.Getwd()
		}
	}
}

// repoRelative is path relative to the repo root as a slash-separated path;
// empty when path lies outside the root.
func repoRelative(outDir, path string) (string, error) {
	root, err := repoRoot(outDir)
	if err != nil {
		return "", err
	}
	// Compare physical paths: /tmp and /private/tmp, or a repo reached
	// through a symlinked parent, are one place.
	if root, err = canonicalPath(root); err != nil {
		return "", err
	}
	abs, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// canonicalPath is the absolute, symlink-free form of path.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func expectFailFlag(on bool) string {
	if on {
		return " --expect-fail"
	}
	return ""
}

// confirmRefresh shows the command a refresh is about to run and asks for
// consent. The command comes from the fixture file, which a pull request can
// change, so it is never run unseen: --yes, or "y" typed at a terminal.
// Without either it refuses (exit 2) and nothing runs. It never asks under CI
// (CI set), with --json, or when stdin is not a terminal.
func confirmRefresh(o *captureOptions, stdin io.Reader, stderr io.Writer) (int, bool) {
	fmt.Fprintf(stderr, "capture: --refresh %s will run, from the fixture's source.command:\n  %s\n", terminalSafe(o.refresh), terminalSafe(shellQuote(o.command)))
	if o.world != "" {
		fmt.Fprintf(stderr, "capture: in the world %s (definition unchanged)\n", terminalSafe(o.worldFile))
	}
	if o.yes {
		return 0, true
	}
	f, isFile := stdin.(*os.File)
	if !mayPrompt(o.json, os.Getenv("CI"), isFile && isTerminal(f)) {
		fmt.Fprintf(stderr, "capture: refusing to run it without confirmation: review the command above (it is source.command in the fixture), then re-run with --yes\n")
		return 2, false
	}
	fmt.Fprint(stderr, "Run it? [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Fprintln(stderr, "capture: not run")
		return 2, false
	}
	return 0, true
}

// mayPrompt reports whether capture may ask a question on stdin: only at a
// terminal, never under CI (CI set to any non-empty value) and never with
// --json, whose caller is a program.
func mayPrompt(asJSON bool, ci string, stdinTTY bool) bool {
	return !asJSON && ci == "" && stdinTTY
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// shellQuote renders argv as one shell command line, quoting every word that
// is not plainly safe.
func shellQuote(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
			words[i] = a
			continue
		}
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}
