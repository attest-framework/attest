package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/attest-ai/attest/engine/internal/selection"
)

func init() {
	register(command{
		name:    "select",
		summary: "list the tests a change needs: --base <ref> [--json] [--repo <dir>]",
		run:     func(args []string) { os.Exit(runSelect(args, os.Stdout, os.Stderr)) },
	})
}

// runSelect prints the selection: one test path per line, `all`, or nothing
// for an empty selection. Exit 2 when git or the ref fails.
func runSelect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("select", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "git ref to diff against (the merge base with HEAD is used)")
	asJSON := fs.Bool("json", false, `print {"mode","tests","reasons"} as JSON`)
	repo := fs.String("repo", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" {
		fmt.Fprintln(stderr, "select: --base is required")
		return 2
	}
	res, err := selection.Select(*repo, *base)
	if err != nil {
		fmt.Fprintf(stderr, "select: %v\n", err)
		return 2
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(stderr, "select: %v\n", err)
			return 2
		}
		return 0
	}
	if res.Mode == selection.ModeAll {
		fmt.Fprintln(stdout, "all")
		return 0
	}
	for _, t := range res.Tests {
		fmt.Fprintln(stdout, t)
	}
	return 0
}
