package cache_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/attest-ai/attest/engine/internal/cache"
)

func newCalibrationStore(t *testing.T) *cache.CalibrationStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := cache.NewCalibrationStore(db)
	if err != nil {
		t.Fatalf("NewCalibrationStore: %v", err)
	}
	return store
}

func TestCalibrationStore_RecordAndRead(t *testing.T) {
	store := newCalibrationStore(t)
	for _, label := range []cache.CalibrationLabel{
		{RubricName: "default", RubricVersion: "v1", PromptHash: "abc",
			Input: "hello", HumanLabel: 0.9, JudgeScore: 0.85},
		{RubricName: "default", RubricVersion: "v1", PromptHash: "abc",
			Input: "world", HumanLabel: 0.2, JudgeScore: 0.3},
	} {
		if err := store.Record(label); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	pairs, err := store.Pairs("default", "v1", "abc")
	if err != nil {
		t.Fatalf("pairs: %v", err)
	}
	if len(pairs) != 2 {
		t.Errorf("expected 2 pairs, got %d", len(pairs))
	}
}

func TestCalibrationStore_KeyIsolation(t *testing.T) {
	store := newCalibrationStore(t)
	_ = store.Record(cache.CalibrationLabel{
		RubricName: "default", RubricVersion: "v1", PromptHash: "abc",
		HumanLabel: 0.9, JudgeScore: 0.9,
	})
	_ = store.Record(cache.CalibrationLabel{
		RubricName: "default", RubricVersion: "v2", PromptHash: "abc",
		HumanLabel: 0.1, JudgeScore: 0.1,
	})
	v1, _ := store.Pairs("default", "v1", "abc")
	v2, _ := store.Pairs("default", "v2", "abc")
	if len(v1) != 1 || v1[0].Human != 0.9 {
		t.Errorf("v1 leaked: %+v", v1)
	}
	if len(v2) != 1 || v2[0].Human != 0.1 {
		t.Errorf("v2 leaked: %+v", v2)
	}
}

func TestCalibrationStore_RejectsInvalidLabel(t *testing.T) {
	store := newCalibrationStore(t)
	cases := []cache.CalibrationLabel{
		{RubricName: "", RubricVersion: "v1", HumanLabel: 0.5, JudgeScore: 0.5},
		{RubricName: "x", RubricVersion: "", HumanLabel: 0.5, JudgeScore: 0.5},
		{RubricName: "x", RubricVersion: "v1", HumanLabel: 1.5, JudgeScore: 0.5},
		{RubricName: "x", RubricVersion: "v1", HumanLabel: 0.5, JudgeScore: -0.1},
	}
	for _, c := range cases {
		if err := store.Record(c); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestCalibrationStore_PairsForRubric(t *testing.T) {
	store := newCalibrationStore(t)
	_ = store.Record(cache.CalibrationLabel{
		RubricName: "default", RubricVersion: "v1", PromptHash: "h1",
		HumanLabel: 0.9, JudgeScore: 0.9,
	})
	_ = store.Record(cache.CalibrationLabel{
		RubricName: "default", RubricVersion: "v1", PromptHash: "h2",
		HumanLabel: 0.1, JudgeScore: 0.1,
	})
	got, err := store.PairsForRubric("default", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 aggregated pairs, got %d", len(got))
	}
}
