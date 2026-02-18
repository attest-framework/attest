package cache_test

import (
	"path/filepath"
	"testing"

	"github.com/attest-ai/attest/engine/internal/cache"
)

func newTestCache(t *testing.T, maxMB int) *cache.EmbeddingCache {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.NewEmbeddingCache(filepath.Join(dir, "test.db"), maxMB)
	if err != nil {
		t.Fatalf("NewEmbeddingCache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestEmbeddingCache_PutGet(t *testing.T) {
	c := newTestCache(t, 10)
	hash := cache.ContentHash("hello world")
	model := "text-embedding-3-small"
	vec := []float32{0.1, 0.2, 0.3, 0.4}

	if err := c.Put(hash, model, vec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get(hash, model)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected cached vector, got nil")
	}
	if len(got) != len(vec) {
		t.Fatalf("vector length: got %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("vector[%d]: got %f, want %f", i, got[i], vec[i])
		}
	}
}

func TestEmbeddingCache_Miss(t *testing.T) {
	c := newTestCache(t, 10)
	got, err := c.Get("nonexistent", "model")
	if err != nil {
		t.Fatalf("Get on miss: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on miss, got %v", got)
	}
}

func TestEmbeddingCache_ModelIsolation(t *testing.T) {
	c := newTestCache(t, 10)
	hash := cache.ContentHash("same text")
	vecA := []float32{1.0, 0.0}
	vecB := []float32{0.0, 1.0}

	if err := c.Put(hash, "model-a", vecA); err != nil {
		t.Fatalf("Put model-a: %v", err)
	}
	if err := c.Put(hash, "model-b", vecB); err != nil {
		t.Fatalf("Put model-b: %v", err)
	}

	gotA, _ := c.Get(hash, "model-a")
	gotB, _ := c.Get(hash, "model-b")

	if gotA == nil || gotA[0] != 1.0 {
		t.Errorf("model-a vector wrong: %v", gotA)
	}
	if gotB == nil || gotB[1] != 1.0 {
		t.Errorf("model-b vector wrong: %v", gotB)
	}
}

func TestEmbeddingCache_Stats(t *testing.T) {
	c := newTestCache(t, 10)
	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats empty: %v", err)
	}
	if stats.Entries != 0 {
		t.Errorf("empty cache entries: got %d, want 0", stats.Entries)
	}

	hash := cache.ContentHash("test")
	if err := c.Put(hash, "model", []float32{1.0, 2.0}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stats, err = c.Stats()
	if err != nil {
		t.Fatalf("Stats after put: %v", err)
	}
	if stats.Entries != 1 {
		t.Errorf("entries after put: got %d, want 1", stats.Entries)
	}
	if stats.TotalBytes == 0 {
		t.Error("TotalBytes should be > 0 after put")
	}
}

func TestEmbeddingCache_Clear(t *testing.T) {
	c := newTestCache(t, 10)
	hash := cache.ContentHash("clear test")
	if err := c.Put(hash, "model", []float32{1.0}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, _ := c.Get(hash, "model")
	if got != nil {
		t.Error("expected nil after clear, got vector")
	}
	stats, _ := c.Stats()
	if stats.Entries != 0 {
		t.Errorf("entries after clear: got %d, want 0", stats.Entries)
	}
}

func TestEmbeddingCache_Eviction(t *testing.T) {
	// maxMB=0 means every insert triggers eviction of older entries
	c := newTestCache(t, 0)

	for i := 0; i < 5; i++ {
		hash := cache.ContentHash(string(rune('a' + i)))
		// Each vector is 4 bytes * 128 = 512 bytes
		vec := make([]float32, 128)
		for j := range vec {
			vec[j] = float32(i)
		}
		if err := c.Put(hash, "model", vec); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	// With maxMB=0, all entries should be evicted after each put
	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Entries > 0 {
		t.Errorf("expected 0 entries with maxMB=0, got %d", stats.Entries)
	}
}

func TestEmbeddingCache_Upsert(t *testing.T) {
	c := newTestCache(t, 10)
	hash := cache.ContentHash("upsert test")
	model := "model"

	if err := c.Put(hash, model, []float32{1.0}); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := c.Put(hash, model, []float32{2.0}); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	got, _ := c.Get(hash, model)
	if got == nil || got[0] != 2.0 {
		t.Errorf("expected updated vector 2.0, got %v", got)
	}

	stats, _ := c.Stats()
	if stats.Entries != 1 {
		t.Errorf("upsert should not create duplicate entries; got %d", stats.Entries)
	}
}

func TestContentHash_Deterministic(t *testing.T) {
	h1 := cache.ContentHash("hello")
	h2 := cache.ContentHash("hello")
	if h1 != h2 {
		t.Error("ContentHash is not deterministic")
	}
}

func TestContentHash_Distinct(t *testing.T) {
	h1 := cache.ContentHash("hello")
	h2 := cache.ContentHash("world")
	if h1 == h2 {
		t.Error("ContentHash should differ for different inputs")
	}
}
