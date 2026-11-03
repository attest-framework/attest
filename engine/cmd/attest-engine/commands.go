package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// command is one attest-engine subcommand. Each command lives in its own file
// and registers itself from init.
type command struct {
	name    string
	summary string
	run     func(args []string)
	// schema is the --json output schema, relative to proto/attest/v1, for a
	// command without subcommands.
	schema string
	// subs lists the subcommands of a command that has them; each carries its
	// own --json output schema.
	subs []subcommand
	// exits declares every exit code the command can return. The table in
	// docs/guides/cli.md is checked against it.
	exits []exitCode
}

// subcommand is one subcommand of a command and the schema of its --json output.
type subcommand struct {
	name   string
	schema string
}

// exitCode is one documented process exit code.
type exitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// Process exit codes shared by the commands: 0 success, 1 the command ran and
// its result is a failure, 2 usage error or invalid input. `policy evaluate`
// has its own set (policy.ExitPass, ExitBlock, ExitWarn, ExitUsage).
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// jsonServeConflict is the usage error for --json combined with --serve:
// --serve already speaks a JSON-lines protocol on stdout.
const jsonServeConflict = "--json applies to -- <command>; --serve already writes JSON lines to stdout"

var commands = map[string]command{}

// register adds c to the command registry. Duplicate names are a programming
// error and panic at startup.
func register(c command) {
	if _, dup := commands[c.name]; dup {
		panic("attest-engine: duplicate command " + c.name)
	}
	commands[c.name] = c
}

// dispatch runs the subcommand selected by args (os.Args[1:]) and reports
// whether it handled the invocation. It returns false when args select the
// JSON-RPC server: no arguments, or a leading flag other than help.
func dispatch(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		if len(args) == 2 && args[0] == "help" && args[1] == "--json" {
			emitJSON(helpDocument())
			os.Exit(0)
		}
		printUsage(os.Stdout)
		os.Exit(0)
	}
	if strings.HasPrefix(args[0], "-") {
		return false
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage(os.Stderr)
		os.Exit(2)
	}
	c.run(args[1:])
	return true
}

// printUsage writes the server invocation and the registered commands to w.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: attest-engine [--log-level=debug|info|warn|error] [--debug]")
	fmt.Fprintln(w, "         start the JSON-RPC 2.0 server on stdin/stdout")
	fmt.Fprintln(w, "       attest-engine <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range names {
		fmt.Fprintf(tw, "  %s\t%s\n", name, commands[name].summary)
	}
	tw.Flush()
}

// helpSubcommand is one subcommand entry of `help --json`.
type helpSubcommand struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
}

// helpCommand is one command entry of `help --json`.
type helpCommand struct {
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	Schema      string           `json:"schema,omitempty"`
	Subcommands []helpSubcommand `json:"subcommands,omitempty"`
	ExitCodes   []exitCode       `json:"exit_codes"`
}

// helpDocument describes every registered command for `help --json`.
func helpDocument() map[string]any {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]helpCommand, 0, len(names))
	for _, name := range names {
		c := commands[name]
		hc := helpCommand{Name: c.name, Summary: c.summary, Schema: c.schema, ExitCodes: c.exits}
		if hc.ExitCodes == nil {
			hc.ExitCodes = []exitCode{}
		}
		for _, s := range c.subs {
			hc.Subcommands = append(hc.Subcommands, helpSubcommand{Name: s.name, Schema: s.schema})
		}
		out = append(out, hc)
	}
	return map[string]any{"commands": out}
}
