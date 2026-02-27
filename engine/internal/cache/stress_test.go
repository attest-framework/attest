package cache_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/attest-ai/attest/engine/internal/cache"
	_ "modernc.org/sqlite"
)

// newTestHistoryStoreFile creates a HistoryStore backed by a file-based SQLite DB
// with busy_timeout to handle contention under concurrent access.
func newTestHistoryStoreFile(t *testing.T) *cache.HistoryStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "history.db")
	dsn := dbPath + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	return store
}

// --- E18: Cache concurrency stress tests ---
//
// These tests verify that the EmbeddingCache and HistoryStore are free of data
// races under concurrent access. Run with -race to catch races.
// SQLite is single-writer; SQLITE_BUSY errors are expected under heavy contention
// and are tolerated — the goal is race detection, not zero-error writes.

// ── EmbeddingCache stress ──

func TestEmbeddingCache_ConcurrentPutGet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.NewEmbeddingCache(filepath.Join(dir, "stress.db"), 100)
	if err != nil {
		t.Fatalf("NewEmbeddingCache: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	const goroutines = 8
	const opsPerGoroutine = 20
	var wg sync.WaitGroup

	// Writer goroutines.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				hash := cache.ContentHash(fmt.Sprintf("stress-%d-%d", gid, i))
				vec := []float32{float32(gid), float32(i), 0.1, 0.2}
				// SQLITE_BUSY is tolerated — race detection is the goal.
				_ = c.Put(hash, "model-stress", vec)
			}
		}(g)
	}

	// Reader goroutines that read while writes happen.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				hash := cache.ContentHash(fmt.Sprintf("stress-%d-%d", gid, i))
				_, _ = c.Get(hash, "model-stress")
			}
		}(g)
	}

	wg.Wait()

	// Verify cache is queryable (not corrupted).
	_, err = c.Stats()
	if err != nil {
		t.Fatalf("Stats after stress: %v", err)
	}
}

func TestEmbeddingCache_ConcurrentEviction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Small maxMB to force frequent evictions.
	c, err := cache.NewEmbeddingCache(filepath.Join(dir, "evict.db"), 0)
	if err != nil {
		t.Fatalf("NewEmbeddingCache: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	const goroutines = 4
	const opsPerGoroutine = 15
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				hash := cache.ContentHash(fmt.Sprintf("evict-%d-%d", gid, i))
				vec := make([]float32, 64) // 256 bytes per vector
				vec[0] = float32(gid)
				_ = c.Put(hash, "model", vec)
			}
		}(g)
	}

	wg.Wait()

	// Verify cache is queryable (not corrupted).
	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats after eviction stress: %v", err)
	}
	// With maxMB=0, entries should mostly be evicted (some may remain from last writer).
	t.Logf("entries after maxMB=0 stress: %d", stats.Entries)
}

func TestEmbeddingCache_DeferredLRUFlushUnderLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.NewEmbeddingCache(filepath.Join(dir, "lru.db"), 100)
	if err != nil {
		t.Fatalf("NewEmbeddingCache: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Pre-populate entries sequentially.
	const entries = 80
	for i := 0; i < entries; i++ {
		hash := cache.ContentHash(fmt.Sprintf("lru-%d", i))
		if err := c.Put(hash, "model", []float32{float32(i), 0.5}); err != nil {
			t.Fatalf("Put lru-%d: %v", i, err)
		}
	}

	// Concurrently read all entries to trigger deferred LRU writes.
	// Reads are non-locking in WAL mode, so this should work well.
	const goroutines = 10
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < entries; i++ {
				hash := cache.ContentHash(fmt.Sprintf("lru-%d", i))
				_, _ = c.Get(hash, "model")
			}
		}()
	}

	wg.Wait()

	// Force flush and verify no data corruption.
	c.FlushLRU()

	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats after LRU stress: %v", err)
	}
	if stats.Entries != entries {
		t.Errorf("entries = %d, want %d after LRU flush stress", stats.Entries, entries)
	}
}

// ── HistoryStore stress ──

func TestHistoryStore_ConcurrentRecord(t *testing.T) {
	t.Parallel()
	store := newTestHistoryStoreFile(t)

	const goroutines = 8
	const recordsPerGoroutine = 25
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < recordsPerGoroutine; i++ {
				assertionID := fmt.Sprintf("assert-%d", gid)
				score := float64(i) / float64(recordsPerGoroutine)
				if err := store.Record(
					fmt.Sprintf("trace-%d-%d", gid, i),
					assertionID,
					"constraint",
					score,
					"pass",
				); err != nil {
					t.Errorf("Record(%d,%d): %v", gid, i, err)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify total records match expectations.
	expected := goroutines * recordsPerGoroutine
	total := 0
	for g := 0; g < goroutines; g++ {
		_, _, count, err := store.Stats(fmt.Sprintf("assert-%d", g))
		if err != nil {
			t.Errorf("Stats assert-%d: %v", g, err)
			continue
		}
		total += count
	}
	if total != expected {
		t.Errorf("total records = %d, want %d", total, expected)
	}
}

func TestHistoryStore_ConcurrentRecordAndQuery(t *testing.T) {
	t.Parallel()
	store := newTestHistoryStoreFile(t)

	const writers = 4
	const readers = 4
	const ops = 20
	var wg sync.WaitGroup

	// Writers.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				_ = store.Record(
					fmt.Sprintf("trace-%d-%d", wid, i),
					"shared-assertion",
					"constraint",
					float64(i)*0.01,
					"pass",
				)
			}
		}(w)
	}

	// Readers.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				// Tolerate errors from SQLite contention; race detection is the goal.
				_, _ = store.QueryWindow("shared-assertion", 10)
				_, _, _, _ = store.Stats("shared-assertion")
			}
		}()
	}

	wg.Wait()
}

func TestHistoryStore_PruneUnderConcurrentRecords(t *testing.T) {
	t.Parallel()
	store := newTestHistoryStoreFile(t)
	// Set aggressive pruning: max 50 rows.
	store.SetPruneConfig(50, 30)

	const goroutines = 5
	const recordsPerGoroutine = 25
	var wg sync.WaitGroup

	// 125 total inserts → triggers prune at 100th insert.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < recordsPerGoroutine; i++ {
				_ = store.Record(
					fmt.Sprintf("trace-%d-%d", gid, i),
					"prune-assert",
					"constraint",
					0.5,
					"pass",
				)
			}
		}(g)
	}

	wg.Wait()

	// After pruning, row count should be bounded. Prune triggers at every 100th insert
	// and races with concurrent inserts, so allow some slack over the configured limit.
	scores, err := store.QueryWindow("prune-assert", 10000)
	if err != nil {
		t.Fatalf("QueryWindow after prune: %v", err)
	}
	// Allow up to 2x the configured max — the important thing is that pruning ran
	// without data races or corruption.
	if len(scores) > 100 {
		t.Errorf("expected <= 100 rows after prune (max configured: 50), got %d", len(scores))
	}
	t.Logf("rows after prune stress: %d (configured max: 50)", len(scores))
}
