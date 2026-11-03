package cache_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/attest-ai/attest/engine/internal/cache"
)

func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// One connection: every pool connection would otherwise get its own
	// in-memory database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newCalibrationStore(t *testing.T) *cache.CalibrationStore {
	t.Helper()
	store, err := cache.NewCalibrationStore(openMemoryDB(t))
	if err != nil {
		t.Fatalf("NewCalibrationStore: %v", err)
	}
	return store
}

// label returns a valid label for the openai/gpt-4.1 judge on rubric
// default/v1; callers override the fields under test.
func label(promptHash string, human, judge float64) cache.CalibrationLabel {
	return cache.CalibrationLabel{
		JudgeProvider: "openai", JudgeModel: "gpt-4.1",
		RubricName: "default", RubricVersion: "v1", PromptHash: promptHash,
		Input: "input-" + promptHash, HumanLabel: human, JudgeScore: judge,
	}
}

var openAIKey = cache.CalibrationKey{
	JudgeProvider: "openai", JudgeModel: "gpt-4.1",
	RubricName: "default", RubricVersion: "v1", PromptHash: "abc",
}

func TestCalibrationStore_RecordAndRead_ReturnsPairsInOrder(t *testing.T) {
	store := newCalibrationStore(t)
	for _, l := range []cache.CalibrationLabel{label("abc", 0.9, 0.85), label("abc", 0.2, 0.3)} {
		if err := store.Record(l); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	pairs, err := store.Pairs(openAIKey)

	if err != nil {
		t.Fatalf("pairs: %v", err)
	}
	if len(pairs) != 2 || pairs[0].Human != 0.9 || pairs[1].Judge != 0.3 {
		t.Errorf("pairs = %+v", pairs)
	}
}

func TestCalibrationStore_Pairs_SeparatesRubricVersions(t *testing.T) {
	store := newCalibrationStore(t)
	v2 := label("abc", 0.1, 0.1)
	v2.RubricVersion = "v2"
	_ = store.Record(label("abc", 0.9, 0.9))
	_ = store.Record(v2)

	v1Pairs, _ := store.Pairs(openAIKey)
	v2Key := openAIKey
	v2Key.RubricVersion = "v2"
	v2Pairs, _ := store.Pairs(v2Key)

	if len(v1Pairs) != 1 || v1Pairs[0].Human != 0.9 {
		t.Errorf("v1 leaked: %+v", v1Pairs)
	}
	if len(v2Pairs) != 1 || v2Pairs[0].Human != 0.1 {
		t.Errorf("v2 leaked: %+v", v2Pairs)
	}
}

func TestCalibrationStore_Pairs_SeparatesJudgeProvidersAndModels(t *testing.T) {
	store := newCalibrationStore(t)
	other := label("abc", 0.1, 0.1)
	other.JudgeProvider, other.JudgeModel = "anthropic", "claude-judge"
	sameProviderOtherModel := label("abc", 0.5, 0.5)
	sameProviderOtherModel.JudgeModel = "gpt-other"
	for _, l := range []cache.CalibrationLabel{label("abc", 0.9, 0.9), other, sameProviderOtherModel} {
		if err := store.Record(l); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	anthropicKey := openAIKey
	anthropicKey.JudgeProvider, anthropicKey.JudgeModel = "anthropic", "claude-judge"

	openAIPairs, _ := store.Pairs(openAIKey)
	anthropicPairs, _ := store.PairsForRubric(anthropicKey)

	if len(openAIPairs) != 1 || openAIPairs[0].Human != 0.9 {
		t.Errorf("openai/gpt-4.1 pairs = %+v, want only its own label", openAIPairs)
	}
	if len(anthropicPairs) != 1 || anthropicPairs[0].Human != 0.1 {
		t.Errorf("anthropic pairs = %+v, want only its own label", anthropicPairs)
	}
}

func TestCalibrationStore_Record_RejectsInvalidLabel(t *testing.T) {
	store := newCalibrationStore(t)
	mutate := []func(*cache.CalibrationLabel){
		func(l *cache.CalibrationLabel) { l.JudgeProvider = "" },
		func(l *cache.CalibrationLabel) { l.JudgeModel = "" },
		func(l *cache.CalibrationLabel) { l.RubricName = "" },
		func(l *cache.CalibrationLabel) { l.RubricVersion = "" },
		func(l *cache.CalibrationLabel) { l.HumanLabel = 1.5 },
		func(l *cache.CalibrationLabel) { l.JudgeScore = -0.1 },
	}
	for i, m := range mutate {
		l := label("abc", 0.5, 0.5)
		m(&l)
		if err := store.Record(l); err == nil {
			t.Errorf("case %d: expected error for %+v", i, l)
		}
	}
}

func TestCalibrationStore_PairsForRubric_AggregatesPromptHashes(t *testing.T) {
	store := newCalibrationStore(t)
	_ = store.Record(label("h1", 0.9, 0.9))
	_ = store.Record(label("h2", 0.1, 0.1))

	got, err := store.PairsForRubric(openAIKey)

	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 aggregated pairs, got %d", len(got))
	}
}

// TestNewCalibrationStore_UpgradesPreProviderSchema opens a database created
// with the calibration_labels schema that had no judge columns: existing rows
// survive with an empty provider and model, stay out of every provider's
// calibration, and the upgrade is idempotent.
func TestNewCalibrationStore_UpgradesPreProviderSchema(t *testing.T) {
	db := openMemoryDB(t)
	if _, err := db.Exec(`
		CREATE TABLE calibration_labels (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			rubric_name     TEXT    NOT NULL,
			rubric_version  TEXT    NOT NULL,
			prompt_hash     TEXT    NOT NULL,
			input           TEXT    NOT NULL,
			human_label     REAL    NOT NULL,
			judge_score     REAL    NOT NULL,
			judged_at       INTEGER NOT NULL
		);
		CREATE INDEX idx_calibration_key ON calibration_labels (rubric_name, rubric_version, prompt_hash);
		INSERT INTO calibration_labels
			(rubric_name, rubric_version, prompt_hash, input, human_label, judge_score, judged_at)
			VALUES ('default', 'v1', 'abc', 'legacy', 0.7, 0.6, 1);
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	store, err := cache.NewCalibrationStore(db)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if _, err := cache.NewCalibrationStore(db); err != nil {
		t.Fatalf("second open is not idempotent: %v", err)
	}
	if err := store.Record(label("abc", 0.9, 0.9)); err != nil {
		t.Fatalf("record after upgrade: %v", err)
	}

	openAIPairs, _ := store.Pairs(openAIKey)
	legacyKey := openAIKey
	legacyKey.JudgeProvider, legacyKey.JudgeModel = "", ""
	legacyPairs, _ := store.Pairs(legacyKey)

	if len(openAIPairs) != 1 || openAIPairs[0].Human != 0.9 {
		t.Errorf("openai pairs = %+v, want the new label only", openAIPairs)
	}
	if len(legacyPairs) != 1 || legacyPairs[0].Human != 0.7 {
		t.Errorf("legacy pairs = %+v, want the migrated row under the empty key", legacyPairs)
	}
}
