package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func init() {
	register(command{
		name:    "cache",
		summary: "show or clear the engine cache (stats|clear)",
		run:     func(args []string) { os.Exit(runCache(args, os.Stdout, os.Stderr)) },
		subs: []subcommand{
			{name: "stats", schema: "cli/cache-stats.schema.json"},
			{name: "clear", schema: "cli/cache-clear.schema.json"},
		},
		exits: []exitCode{
			{exitOK, "success"},
			{exitFailure, "the cache directory could not be read, or the home directory could not be determined"},
			{exitUsage, "usage error"},
		},
	})
}

const cacheUsage = "usage: attest-engine cache <stats|clear> [--json]"

// cacheDir returns the cache directory from ATTEST_CACHE_DIR env or ~/.attest/cache/.
func cacheDir() string {
	if dir := os.Getenv("ATTEST_CACHE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(exitFailure)
	}
	return filepath.Join(home, ".attest", "cache")
}

// runCache handles: attest-engine cache stats | cache clear, each with an
// optional --json.
func runCache(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, cacheUsage)
		return exitUsage
	}
	sub := args[0]
	if sub != "stats" && sub != "clear" {
		fmt.Fprintf(stderr, "unknown cache command: %s\n%s\n", sub, cacheUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("cache "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, cacheUsage)
		return exitUsage
	}
	dir := cacheDir()
	if sub == "stats" {
		return cacheStats(dir, *asJSON, stdout, stderr)
	}
	return cacheClear(dir, *asJSON, stdout, stderr)
}

func cacheStats(dir string, asJSON bool, stdout, stderr io.Writer) int {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		if asJSON {
			return writeJSONResult(stdout, stderr, "cache stats", map[string]any{
				"dir": dir, "exists": false, "files": 0, "size_bytes": 0,
			})
		}
		fmt.Fprintln(stdout, "cache directory does not exist:", dir)
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "stat cache dir: %v\n", err)
		return exitFailure
	}

	// Walk and sum sizes
	var totalBytes int64
	var fileCount int
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "read cache dir: %v\n", err)
		return exitFailure
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fi, err := entry.Info()
		if err != nil {
			continue
		}
		totalBytes += fi.Size()
		fileCount++
	}
	if asJSON {
		return writeJSONResult(stdout, stderr, "cache stats", map[string]any{
			"dir": dir, "exists": true, "files": fileCount, "size_bytes": totalBytes,
		})
	}
	fmt.Fprintf(stdout, "cache dir: %s\n", dir)
	fmt.Fprintf(stdout, "exists:    true\n")
	fmt.Fprintf(stdout, "is_dir:    %v\n", info.IsDir())
	fmt.Fprintf(stdout, "files:     %d\n", fileCount)
	fmt.Fprintf(stdout, "size_bytes: %d\n", totalBytes)
	return exitOK
}

func cacheClear(dir string, asJSON bool, stdout, stderr io.Writer) int {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if asJSON {
			return writeJSONResult(stdout, stderr, "cache clear", map[string]any{"dir": dir, "deleted": 0})
		}
		fmt.Fprintln(stdout, "cache directory does not exist:", dir)
		return exitOK
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "read cache dir: %v\n", err)
		return exitFailure
	}
	var deleted int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(stderr, "remove %s: %v\n", path, err)
			continue
		}
		deleted++
	}
	if asJSON {
		return writeJSONResult(stdout, stderr, "cache clear", map[string]any{"dir": dir, "deleted": deleted})
	}
	fmt.Fprintf(stdout, "cleared %d file(s) from %s\n", deleted, dir)
	return exitOK
}

// writeJSONResult writes v as the command's one JSON document and returns
// exitOK, or reports the write failure and returns exitFailure.
func writeJSONResult(stdout, stderr io.Writer, name string, v any) int {
	if err := writeJSON(stdout, v); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return exitFailure
	}
	return exitOK
}
