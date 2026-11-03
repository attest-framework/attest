package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/attest-ai/attest/engine/internal/buildinfo"
	attestmcp "github.com/attest-ai/attest/engine/internal/mcp"
	"github.com/attest-ai/attest/engine/internal/pathguard"
)

func init() {
	register(command{
		name:    "mcp",
		summary: "serve attest to coding agents over MCP on stdio, list its tools, or write client configs: [--root D] | --tools [--json] | install --dir D --command <json argv>",
		run:     func(args []string) { os.Exit(runMCP(args)) },
		subs: []subcommand{
			{name: "tools", schema: "cli/mcp-tools.schema.json"},
			{name: "install", schema: "cli/mcp-install.schema.json"},
		},
		exits: []exitCode{
			{Code: 0, Meaning: "the client disconnected, the tools were listed, or the client configs were written"},
			{Code: 1, Meaning: "the server failed, the project root is unusable, or a config could not be read or written"},
			{Code: 2, Meaning: "usage error"},
		},
	})
}

const mcpUsage = `usage: attest-engine mcp [--root DIR]
       attest-engine mcp --tools [--json]
       attest-engine mcp install --dir DIR --command '<json argv>' [--force] [--json]`

func runMCP(args []string) int {
	if len(args) > 0 && args[0] == "install" {
		return runMCPInstall(args[1:])
	}
	if len(args) > 0 && args[0] == "tools" {
		args = append([]string{"--tools"}, args[1:]...)
	}
	fset := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fset.SetOutput(os.Stderr)
	root := fset.String("root", ".", "project root the server may read (default: the working directory)")
	listTools := fset.Bool("tools", false, "print the fixed tool list and exit")
	asJSON := fset.Bool("json", false, "with --tools: print the tool list as JSON")
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fset.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "mcp: unexpected argument %q\n%s\n", fset.Arg(0), mcpUsage)
		return 2
	}
	if *asJSON && !*listTools {
		fmt.Fprintf(os.Stderr, "mcp: --json applies to --tools and install; the server speaks MCP on stdout\n%s\n", mcpUsage)
		return 2
	}
	if *listTools {
		return printMCPTools(*asJSON)
	}

	r, err := pathguard.NewRoot(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "attest mcp: serving %s on stdio\n", r.Path())
	if err := attestmcp.Serve(ctx, r, buildinfo.Version, os.Stderr); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}

func printMCPTools(asJSON bool) int {
	tools := attestmcp.Tools()
	if asJSON {
		emitJSON(map[string]any{"tools": tools})
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range tools {
		fmt.Fprintf(tw, "%s\t%s\n", t.Name, t.Description)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}

func runMCPInstall(args []string) int {
	fset := flag.NewFlagSet("mcp install", flag.ContinueOnError)
	fset.SetOutput(os.Stderr)
	dir := fset.String("dir", ".", "project directory to write the client configs into")
	command := fset.String("command", "", "launch command as a JSON array of strings, for example '[\"npx\",\"attest\",\"mcp\"]'")
	force := fset.Bool("force", false, "replace an existing, different attest entry")
	asJSON := fset.Bool("json", false, `print {"dir","created","updated","unchanged","conflict"} as JSON`)
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fset.NArg() > 0 || *command == "" {
		fmt.Fprintf(os.Stderr, "mcp install: --command is required and takes no positional arguments\n%s\n", mcpUsage)
		return 2
	}
	var argv []string
	dec := json.NewDecoder(strings.NewReader(*command))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&argv); err != nil || dec.More() {
		fmt.Fprintf(os.Stderr, "mcp install: --command must be a JSON array of strings\n")
		return 2
	}
	res, err := attestmcp.Install(*dir, argv, *force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp install: %v\n", err)
		return 1
	}
	if *asJSON {
		emitJSON(res)
	} else {
		fmt.Printf("wrote attest MCP configs into %s: %d created, %d updated, %d unchanged, %d conflict\n",
			res.Dir, len(res.Created), len(res.Updated), len(res.Unchanged), len(res.Conflict))
	}
	if len(res.Conflict) > 0 {
		fmt.Fprintf(os.Stderr, "mcp install: left a different attest entry in %v; re-run with --force to replace it\n", res.Conflict)
	}
	return 0
}
