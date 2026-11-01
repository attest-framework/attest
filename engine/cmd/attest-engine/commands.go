package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
)

// command is one attest-engine subcommand. Each command lives in its own file
// and registers itself from init.
type command struct {
	name    string
	summary string
	run     func(args []string)
}

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
		printUsage(os.Stdout)
		os.Exit(0)
	}
	if args[0][0] == '-' {
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
