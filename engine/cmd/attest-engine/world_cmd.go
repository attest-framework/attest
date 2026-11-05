package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/attest-framework/attest/engine/internal/capture"
	"github.com/attest-framework/attest/engine/internal/world"
)

func init() {
	register(command{
		name:    "world",
		summary: "Serve a world.yaml as a local stateful HTTP fake",
		run:     handleWorldCommand,
		schema:  "cli/world.schema.json",
		exits: []exitCode{
			{exitOK, "the command exited 0, or --serve stopped cleanly"},
			{exitFailure, "the command exited non-zero (its code is not propagated; --json reports it as exit_code), or the world could not be served or its state written"},
			{exitUsage, "usage error or an invalid world.yaml"},
		},
	})
}

const worldUsage = `usage: attest-engine world --file <world.yaml> [options] -- <command> [args...]
       attest-engine world --file <world.yaml> --serve [options]

Serves the external system world.yaml declares (JSON resources with create,
list and get routes, idempotency-key handling, initial state and a seeded
fault script) on 127.0.0.1, and runs <command> with the variable the file
names (base_url_env) set to its base URL. GET <base URL>/_attest/state returns
the world's current state as a world-v1 snapshot.

options:
  --file <world.yaml>    the world definition (world-v1); a malformed file
                         exits 2 naming the offending path
  --state-out <path>     write the final world-v1 snapshot as JSON to <path>
  --serve                for an SDK helper: print {"env":{...}} as one JSON
                         line, serve until stdin sends "stop" or closes, then
                         print {"world":{...}} (the final snapshot) and exit
  --json                 with -- <command>: print one JSON result object
                         {"exit_code","world"} to stdout; the command's
                         stdout goes to stderr`

type worldOptions struct {
	file, stateOut string
	serve, json    bool
	command        []string
}

func handleWorldCommand(args []string) {
	os.Exit(runWorld(args, os.Stdin, os.Stdout, os.Stderr))
}

func parseWorldArgs(args []string, stderr io.Writer) (*worldOptions, bool) {
	o := &worldOptions{}
	fs := flag.NewFlagSet("world", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.file, "file", "", "")
	fs.StringVar(&o.stateOut, "state-out", "", "")
	fs.BoolVar(&o.serve, "serve", false, "")
	fs.BoolVar(&o.json, "json", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "world: %s\n%s\n", terminalSafe(err.Error()), worldUsage)
		return nil, false
	}
	o.command = fs.Args()
	usage := func(msg string) (*worldOptions, bool) {
		fmt.Fprintf(stderr, "world: %s\n%s\n", msg, worldUsage)
		return nil, false
	}
	switch {
	case o.file == "":
		return usage("--file is required")
	case o.serve == (len(o.command) > 0):
		return usage("give exactly one of --serve or -- <command>")
	case o.json && o.serve:
		return usage(jsonServeConflict)
	}
	return o, true
}

// loadWorld reads and validates a world.yaml and builds its world.
func loadWorld(path string) (*world.World, error) {
	def, err := world.LoadFile(path)
	if err != nil {
		return nil, err
	}
	return world.New(def)
}

func runWorld(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := parseWorldArgs(args, stderr)
	if !ok {
		return 2
	}
	w, err := loadWorld(o.file)
	if err != nil {
		printWorldError(stderr, "world", o.file, err)
		return 2
	}
	s, err := capture.StartWorld(w)
	if err != nil {
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	if o.serve {
		return worldServe(s, o, stdin, stdout, stderr)
	}
	return worldCommand(s, o, stdout, stderr)
}

func worldServe(s *capture.Session, o *worldOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := writeJSONLine(stdout, map[string]any{"env": s.Env()}); err != nil {
		_ = s.Close()
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "stop" {
			break
		}
	}
	if err := s.Close(); err != nil {
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	snap := s.WorldSnapshot()
	if code := writeStateOut(o.stateOut, snap, stderr); code != 0 {
		return code
	}
	if err := writeJSONLine(stdout, map[string]any{"world": snap}); err != nil {
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	return 0
}

func worldCommand(s *capture.Session, o *worldOptions, stdout, stderr io.Writer) int {
	argv := o.command
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = withEnv(os.Environ(), s.Env())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if o.json {
		cmd.Stdout = os.Stderr
	}
	stopForward, err := startForwardingSignals(cmd)
	if err != nil {
		_ = s.Close()
		fmt.Fprintf(stderr, "world: running %s: %s\n", terminalSafe(argv[0]), terminalSafe(err.Error()))
		return 1
	}
	runErr := cmd.Wait()
	stopForward()
	if err := s.Close(); err != nil {
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	snap := s.WorldSnapshot()
	writeWorldSummary(stderr, snap)
	if code := writeStateOut(o.stateOut, snap, stderr); code != 0 {
		return code
	}
	if o.json {
		if err := writeJSON(stdout, map[string]any{"exit_code": childExitCode(runErr), "world": snap}); err != nil {
			fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
			return 1
		}
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(runErr, &exitErr):
		fmt.Fprintf(stderr, "world: command exited %d\n", exitErr.ExitCode())
		return 1
	case runErr != nil:
		fmt.Fprintf(stderr, "world: running %s: %s\n", terminalSafe(argv[0]), terminalSafe(runErr.Error()))
		return 1
	}
	return 0
}

// writeWorldSummary prints the one-line world summary: record counts and the
// faults that fired.
func writeWorldSummary(w io.Writer, snap *world.Snapshot) {
	fired := snap.FaultsFiredCount
	fmt.Fprintf(w, "world: %s · %d %s fired\n", strings.Join(snap.Counts(), " · "), fired, plural(fired, "fault", "faults"))
}

func writeStateOut(path string, snap *world.Snapshot, stderr io.Writer) int {
	if path == "" {
		return 0
	}
	if err := writeJSONFile(path, snap); err != nil {
		fmt.Fprintf(stderr, "world: %s\n", terminalSafe(err.Error()))
		return 1
	}
	return 0
}

// printWorldError prints a world definition failure, one terminal-safe line
// per problem.
func printWorldError(w io.Writer, cmd, path string, err error) {
	for _, line := range world.Errors(err) {
		fmt.Fprintf(w, "%s: %s: %s\n", cmd, terminalSafe(path), terminalSafe(line))
	}
}
