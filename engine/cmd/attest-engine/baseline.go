package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/internal/report"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func init() {
	register(command{
		name:    "baseline",
		summary: "pin, list, show or delete report baselines (pin|list|show|delete)",
		run:     handleBaselineCommand,
		subs: []subcommand{
			{name: "pin", schema: "cli/baseline-pin.schema.json"},
			{name: "list", schema: "cli/baseline-list.schema.json"},
			{name: "show", schema: "cli/baseline-show.schema.json"},
			{name: "delete", schema: "cli/baseline-delete.schema.json"},
		},
		exits: []exitCode{
			{exitOK, "success"},
			{exitFailure, "the report could not be loaded or has no assertion results, the tag is not pinned (show), or the baseline store failed"},
			{exitUsage, "usage error"},
		},
	})
}

// baselineFlags is the flag set of one baseline subcommand. Every subcommand
// prints JSON; --json is accepted for uniformity with the other commands.
func baselineFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("baseline "+name, flag.ExitOnError)
	fs.Bool("json", false, "print the result as JSON (always on)")
	return fs
}

// parseBaselineFlags parses args and refuses positional arguments.
func parseBaselineFlags(fs *flag.FlagSet, args []string) {
	if err := fs.Parse(args); err != nil {
		os.Exit(exitUsage)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		baselineUsage()
		os.Exit(exitUsage)
	}
}

// handleBaselineCommand dispatches `attest-engine baseline pin|list|show|delete`.
// Baselines are persisted in $ATTEST_CACHE_DIR/attest.db via cache.BaselineStore.
func handleBaselineCommand(args []string) {
	if len(args) == 0 {
		baselineUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "pin":
		baselinePin(args[1:])
	case "list":
		baselineList(args[1:])
	case "show":
		baselineShow(args[1:])
	case "delete":
		baselineDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown baseline subcommand: %s\n", args[0])
		baselineUsage()
		os.Exit(2)
	}
}

func baselineUsage() {
	fmt.Fprintln(os.Stderr, "usage: attest-engine baseline <pin|list|show|delete> [args]")
	fmt.Fprintln(os.Stderr, "  pin    --tag <name> --report <path>")
	fmt.Fprintln(os.Stderr, "  list")
	fmt.Fprintln(os.Stderr, "  show   --tag <name>")
	fmt.Fprintln(os.Stderr, "  delete --tag <name>")
}

// openBaselineStore opens (and creates if absent) the SQLite cache DB and
// returns a BaselineStore plus a closer the caller must defer.
func openBaselineStore() (*cache.BaselineStore, func(), error) {
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("mkdir cache: %w", err)
	}
	dbPath := filepath.Join(dir, "attest.db")
	db, release, err := cache.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.NewBaselineStore(db)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("baseline store: %w", err)
	}
	return store, func() { release() }, nil
}

// reportEnvelope is the subset of JSONReportV2 needed to build baseline
// entries — assertion-level rows plus the totals block. We accept v2
// reports only because v1 lacks Layer/Type/FailureClass.
type reportEnvelope struct {
	ReportVersion   int                     `json:"report_version"`
	Results         []types.AssertionResult `json:"results"`
	TotalCost       float64                 `json:"total_cost"`
	TotalDurationMS int64                   `json:"total_duration_ms"`
}

func loadReport(path string) (*reportEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	var env reportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode report %s: %w", path, err)
	}
	if env.ReportVersion != 2 {
		return nil, fmt.Errorf("report %s is version %d; expected 2 (run with default report version)", path, env.ReportVersion)
	}
	return &env, nil
}

func baselinePin(args []string) {
	fs := baselineFlags("pin")
	tag := fs.String("tag", "", "baseline tag (e.g. release name or git SHA)")
	reportPath := fs.String("report", "", "path to JSON v2 report to snapshot")
	parseBaselineFlags(fs, args)
	if *tag == "" || *reportPath == "" {
		fmt.Fprintln(os.Stderr, "baseline pin: --tag and --report are required")
		os.Exit(2)
	}

	env, err := loadReport(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline pin: %v\n", err)
		os.Exit(1)
	}
	if len(env.Results) == 0 {
		fmt.Fprintf(os.Stderr, "baseline pin: report %s has no assertion results\n", *reportPath)
		os.Exit(1)
	}

	entries := report.BaselineEntries(env.Results)

	store, closer, err := openBaselineStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline pin: %v\n", err)
		os.Exit(1)
	}
	defer closer()

	if err := store.Pin(*tag, entries); err != nil {
		fmt.Fprintf(os.Stderr, "baseline pin: %v\n", err)
		os.Exit(1)
	}
	out := map[string]any{
		"tag":             *tag,
		"assertion_count": len(entries),
		"pinned_at":       time.Now().UTC().Format(time.RFC3339),
	}
	emitJSON(out)
}

func baselineList(args []string) {
	parseBaselineFlags(baselineFlags("list"), args)
	store, closer, err := openBaselineStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline list: %v\n", err)
		os.Exit(1)
	}
	defer closer()

	summaries, err := store.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline list: %v\n", err)
		os.Exit(1)
	}

	out := make([]map[string]any, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, map[string]any{
			"tag":               s.Tag,
			"assertion_count":   s.AssertionCount,
			"passed":            s.Passed,
			"soft_failed":       s.SoftFailed,
			"hard_failed":       s.HardFailed,
			"total_cost":        s.TotalCost,
			"total_duration_ms": s.TotalDurationMS,
			"pinned_at":         s.PinnedAt.Format(time.RFC3339),
		})
	}
	emitJSON(out)
}

func baselineShow(args []string) {
	fs := baselineFlags("show")
	tag := fs.String("tag", "", "baseline tag")
	parseBaselineFlags(fs, args)
	if *tag == "" {
		fmt.Fprintln(os.Stderr, "baseline show: --tag is required")
		os.Exit(2)
	}
	store, closer, err := openBaselineStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline show: %v\n", err)
		os.Exit(1)
	}
	defer closer()
	entries, err := store.Get(*tag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline show: %v\n", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "baseline show: no baseline pinned with tag %q\n", *tag)
		os.Exit(1)
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, map[string]any{
			"assertion_id":  e.AssertionID,
			"type":          e.Type,
			"status":        e.Status,
			"score":         e.Score,
			"cost":          e.Cost,
			"duration_ms":   e.DurationMS,
			"passes":        e.Passes,
			"evaluated":     e.Evaluated,
			"not_evaluated": e.NotEvaluated,
		})
	}
	emitJSON(map[string]any{
		"tag":     *tag,
		"entries": rows,
	})
}

func baselineDelete(args []string) {
	fs := baselineFlags("delete")
	tag := fs.String("tag", "", "baseline tag")
	parseBaselineFlags(fs, args)
	if *tag == "" {
		fmt.Fprintln(os.Stderr, "baseline delete: --tag is required")
		os.Exit(2)
	}
	store, closer, err := openBaselineStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline delete: %v\n", err)
		os.Exit(1)
	}
	defer closer()
	n, err := store.Delete(*tag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline delete: %v\n", err)
		os.Exit(1)
	}
	emitJSON(map[string]any{
		"tag":     *tag,
		"deleted": n,
	})
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(1)
	}
}

// computeBaselineDelta loads a tag and diffs it against report results.
// Used by both `attest-engine report --vs <tag>` (rendering) and
// `attest-engine policy evaluate --baseline <tag>` (gating).
func computeBaselineDelta(tag string, env *reportEnvelope) (*report.BaselineDelta, error) {
	store, closer, err := openBaselineStore()
	if err != nil {
		return nil, err
	}
	defer closer()
	entries, err := store.Get(tag)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no baseline pinned with tag %q", tag)
	}
	d := report.ComputeBaselineDelta(tag, entries, env.Results)
	return &d, nil
}
