package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/attest-ai/attest/engine/internal/buildinfo"
	"github.com/attest-ai/attest/engine/internal/server"
)

func init() {
	register(command{
		name:    "version",
		summary: "print the engine version",
		run:     func(args []string) { os.Exit(runVersion(args, os.Stdout, os.Stderr)) },
		schema:  "cli/version.schema.json",
		exits: []exitCode{
			{exitOK, "success"},
			{exitFailure, "writing the output failed"},
			{exitUsage, "usage error"},
		},
	})
}

// runVersion prints `attest-engine <version>`, or with --json
// {"engine_version", "protocol_version"}.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the version as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: attest-engine version [--json]")
		return exitUsage
	}
	if *asJSON {
		if err := writeJSON(stdout, map[string]any{
			"engine_version":   buildinfo.Version,
			"protocol_version": server.ProtocolVersion,
		}); err != nil {
			fmt.Fprintf(stderr, "version: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "attest-engine %s\n", buildinfo.Version)
	return exitOK
}
