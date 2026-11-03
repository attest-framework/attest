package main_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func writeLabels(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labels.csv")
	src := "input,human_label,judge_score\nhelpful answer,1,0.9\nrude answer,0,0.2\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCalibrate_PersistWithoutJudge_ExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	labels := writeLabels(t)
	cases := map[string][]string{
		"missing provider and model": {"--persist"},
		"missing model":              {"--persist", "--judge-provider", "openai"},
		"unsupported provider":       {"--persist", "--judge-provider", "OpenAI", "--judge-model", "gpt-4.1"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"calibrate", "--labels", labels}, extra...)

			_, stderr, code := runEngine(t, bin, t.TempDir(), args...)

			if code != 2 || !strings.Contains(stderr, "--judge-") {
				t.Fatalf("exit %d stderr %q, want 2 naming the judge flags", code, stderr)
			}
		})
	}
}

func TestCalibrate_Persist_StoresLabelsKeyedByJudge(t *testing.T) {
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()

	stdout, stderr, code := runEngine(t, bin, cacheDir, "calibrate", "--labels", writeLabels(t),
		"--persist", "--judge-provider", "anthropic", "--judge-model", "judge-model")

	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if !strings.Contains(stdout, `"judge_provider": "anthropic"`) || !strings.Contains(stdout, `"judge_model": "judge-model"`) {
		t.Errorf("output does not name the judge:\n%s", stdout)
	}
	db, err := sql.Open("sqlite", filepath.Join(cacheDir, "attest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM calibration_labels
		WHERE judge_provider = 'anthropic' AND judge_model = 'judge-model'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("stored %d rows under anthropic/judge-model, want 2", n)
	}
}
