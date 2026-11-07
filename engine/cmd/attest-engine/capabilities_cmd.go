package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/attest-framework/attest/engine/internal/capabilities"
)

func init() {
	register(command{
		name:    "capabilities",
		summary: "print the layers, assertion types, adapters and maturity attest ships (--json)",
		run:     func(args []string) { os.Exit(runCapabilities(args, os.Stdout, os.Stderr)) },
		schema:  "cli/capabilities.schema.json",
		exits: []exitCode{
			{exitOK, "success"},
			{exitFailure, "writing the output failed"},
			{exitUsage, "usage error"},
		},
	})
}

// runCapabilities prints the capabilities document: a table summary, or with
// --json the document the README and guide tables are generated from.
func runCapabilities(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the capabilities as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: attest-engine capabilities [--json]")
		return exitUsage
	}
	doc, err := capabilities.Build()
	if err != nil {
		fmt.Fprintf(stderr, "capabilities: %v\n", err)
		return exitFailure
	}
	if *asJSON {
		return writeJSONResult(stdout, stderr, "capabilities", doc)
	}
	if err := printCapabilities(stdout, doc); err != nil {
		fmt.Fprintf(stderr, "capabilities: %v\n", err)
		return exitFailure
	}
	return exitOK
}

func printCapabilities(w io.Writer, doc capabilities.Document) error {
	fmt.Fprintf(w, "attest-engine %s (protocol %d), license %s\n", doc.EngineVersion, doc.ProtocolVersion, doc.License)
	fmt.Fprintf(w, "SDK languages: %s\n\n", strings.Join(doc.SDKLanguages, ", "))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "LAYER\tNAME\tCOST\tDETERMINISTIC\tASSERTION TYPES")
	for _, l := range doc.Layers {
		id := fmt.Sprint(l.ID)
		if l.Name == "Plugins" {
			id = "plugins"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n", id, l.Name, l.Cost, l.Deterministic, strings.Join(l.AssertionTypes, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, sdk := range []struct {
		name     string
		adapters []capabilities.Adapter
	}{{"Python", doc.Adapters.Python}, {"TypeScript", doc.Adapters.TypeScript}} {
		fmt.Fprintf(w, "\n%s adapters\n", sdk.name)
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, a := range sdk.adapters {
			fmt.Fprintf(tw, "  %s\t%s\n", a.Name, a.Maturity)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintln(w, "\nFeatures")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, f := range doc.Features {
		fmt.Fprintf(tw, "  %s\t%s\n", f.Name, f.Maturity)
	}
	return tw.Flush()
}
