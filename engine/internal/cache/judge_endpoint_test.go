package cache

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestJudgeCache_SameModelDifferentEndpoints_DoNotShareScores(t *testing.T) {
	db := filepath.Join(t.TempDir(), "attest.db")
	openai, err := NewJudgeCache(db, 100, "https://api.openai.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer openai.Close()
	local, err := NewJudgeCache(db, 100, "http://localhost:11434/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	hash := JudgePromptKey("v2", "refund processed")

	if err := openai.Put(hash, "default", "gpt-4.1", &JudgeCacheEntry{Score: 0.9, Explanation: "openai"}); err != nil {
		t.Fatal(err)
	}

	got, err := local.Get(hash, "default", "gpt-4.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("local endpoint returned the OpenAI endpoint's score: %+v", got)
	}
	same, err := openai.Get(hash, "default", "gpt-4.1")
	if err != nil || same == nil || same.Score != 0.9 {
		t.Fatalf("same endpoint lookup = %+v (err %v), want the stored 0.9", same, err)
	}
}

func TestNewJudgeCache_EmptyEndpoint_Errors(t *testing.T) {
	_, err := NewJudgeCache(filepath.Join(t.TempDir(), "attest.db"), 100, "")

	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("err = %v, want endpoint error", err)
	}
}
