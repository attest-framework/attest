package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
	"github.com/attest-ai/attest/engine/internal/cache"
)

// handleCalibrateCommand runs `attest-engine calibrate --labels <path>
// --rubric <name> [--threshold 0.5] [--persist]`. It expects each row in
// the labels file to already include both human_label and judge_score —
// invocation does NOT call the judge model. This keeps the command
// dependency-free for offline use; agreement runs in CI without LLM keys.
func handleCalibrateCommand(args []string) {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	labels := fs.String("labels", "", "path to CSV or JSONL file with human_label and judge_score columns")
	rubric := fs.String("rubric", "default", "rubric name to associate calibration data with")
	rubricVer := fs.String("rubric-version", "", "rubric version (defaults to built-in version when --rubric is built-in)")
	threshold := fs.Float64("threshold", 0.5, "binarization threshold for κ and ROC-AUC (must be in (0,1))")
	persist := fs.Bool("persist", false, "store the loaded labels in the calibration history at $ATTEST_CACHE_DIR/attest.db")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *labels == "" {
		fmt.Fprintln(os.Stderr, "calibrate: --labels is required")
		os.Exit(2)
	}

	registry := judge.NewRubricRegistry()
	resolvedVersion := *rubricVer
	if resolvedVersion == "" {
		rb, err := registry.Get(*rubric)
		if err != nil {
			fmt.Fprintf(os.Stderr, "calibrate: --rubric-version is required for unknown rubric %q\n", *rubric)
			os.Exit(2)
		}
		resolvedVersion = rb.Version
	}

	records, err := loadLabelsByExt(*labels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibrate: %v\n", err)
		os.Exit(1)
	}

	pairs := make([]judge.LabelPair, 0, len(records))
	missingJudge := 0
	for _, r := range records {
		if !r.JudgeKnown {
			missingJudge++
			continue
		}
		pairs = append(pairs, judge.LabelPair{Human: r.HumanLabel, Judge: r.JudgeScore})
	}
	if len(pairs) == 0 {
		fmt.Fprintf(os.Stderr, "calibrate: no rows had judge_score (%d rows missing); pre-score the labels file first\n", missingJudge)
		os.Exit(1)
	}

	agreement, err := judge.ComputeAgreement(pairs, *threshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calibrate: %v\n", err)
		os.Exit(1)
	}

	if *persist {
		if err := persistLabels(records, *rubric, resolvedVersion); err != nil {
			fmt.Fprintf(os.Stderr, "calibrate: persist failed: %v\n", err)
			os.Exit(1)
		}
	}

	out := map[string]any{
		"rubric_name":    *rubric,
		"rubric_version": resolvedVersion,
		"threshold":      agreement.Threshold,
		"label_count":    agreement.N,
		"agreement":      round3(agreement.Agreement),
		"cohen_kappa":    round3(agreement.CohenKappa),
		"roc_auc":        round3(agreement.ROCAUC),
		"persisted":      *persist,
		"missing_judge":  missingJudge,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "calibrate: encode result: %v\n", err)
		os.Exit(1)
	}
}

func loadLabelsByExt(path string) ([]judge.LabeledRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open labels: %w", err)
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return judge.LoadLabelsCSV(f)
	case ".jsonl", ".ndjson":
		return judge.LoadLabelsJSONL(f)
	default:
		buf, readErr := io.ReadAll(f)
		if readErr != nil {
			return nil, fmt.Errorf("read labels: %w", readErr)
		}
		trimmed := strings.TrimSpace(string(buf))
		if strings.HasPrefix(trimmed, "{") {
			return judge.LoadLabelsJSONL(strings.NewReader(string(buf)))
		}
		return judge.LoadLabelsCSV(strings.NewReader(string(buf)))
	}
}

func persistLabels(records []judge.LabeledRecord, rubricName, rubricVersion string) error {
	dbPath := filepath.Join(cacheDir(), "attest.db")
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return fmt.Errorf("mkdir cache: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()
	store, err := cache.NewCalibrationStore(db)
	if err != nil {
		return fmt.Errorf("calibration store: %w", err)
	}
	now := time.Now()
	for _, r := range records {
		if !r.JudgeKnown {
			continue
		}
		if err := store.Record(cache.CalibrationLabel{
			RubricName:    rubricName,
			RubricVersion: rubricVersion,
			PromptHash:    judge.PromptHash(r.Input),
			Input:         r.Input,
			HumanLabel:    r.HumanLabel,
			JudgeScore:    r.JudgeScore,
			JudgedAt:      now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func round3(x float64) float64 {
	return float64(int(x*1000+0.5)) / 1000
}
