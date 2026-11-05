package cache_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/cache"
)

// seedHistory bulk-inserts ids x perID rows in one transaction. Timestamps are
// recent so only the row-count prune has work to do.
func seedHistory(t *testing.T, db *sql.DB, ids, perID int) {
	t.Helper()
	now := time.Now().UnixNano()
	if _, err := db.Exec(`
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO assertion_history (trace_id, assertion_id, assertion_type, score, status, created_at)
		SELECT 'seed', 'seed-' || (i % ?), 'constraint', 0.5, 'pass', ? - i FROM n`,
		ids*perID-1, ids, now,
	); err != nil {
		t.Fatalf("seed history: %v", err)
	}
}

// A prune over a large table must never hold the write lock for the whole
// scan: a Record issued while the prune runs completes far inside the busy
// timeout. The previous full-table window delete held the lock for the entire
// scan (1.35 s at 1M rows), stalling every concurrent writer.
func TestHistoryStore_PruneLargeTable_ConcurrentRecordDoesNotWait(t *testing.T) {
	// Arrange
	db, release, err := cache.Open(t.TempDir() + "/attest.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer release()
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	seedHistory(t, db, 100, seedPerID)

	// Act
	pruneDone := make(chan error, 1)
	go func() { pruneDone <- store.Prune(seedPerID-100, 30) }()
	var worst time.Duration
	var recorded int
	for running := true; running; {
		select {
		case err := <-pruneDone:
			if err != nil {
				t.Fatalf("Prune: %v", err)
			}
			running = false
		default:
			start := time.Now()
			if err := store.Record("trace-live", "live-assertion", "constraint", 1, "pass"); err != nil {
				t.Fatalf("Record during prune: %v", err)
			}
			if d := time.Since(start); d > worst {
				worst = d
			}
			recorded++
			time.Sleep(time.Millisecond) // one request thread, not a tight loop
		}
	}
	store.WaitForPrune()

	// Assert
	t.Logf("%d records during prune, worst latency %s", recorded, worst)
	if worst > maxRecordWait {
		t.Fatalf("worst Record latency during prune = %s, want <= %s", worst, maxRecordWait)
	}
	var seedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assertion_history WHERE assertion_id LIKE 'seed-%'`).Scan(&seedRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := 100 * (seedPerID - 100); seedRows != want {
		t.Fatalf("seed rows after prune = %d, want %d", seedRows, want)
	}
}

// Only ids recorded since the last prune are pruned by the background path
// after the first run, so a trigger never rescans the whole table.
func TestHistoryStore_BackgroundPrune_TrimsRecordedIDToMaxRows(t *testing.T) {
	// Arrange
	db, release, err := cache.Open(t.TempDir() + "/attest.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer release()
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	store.SetPruneConfig(150, 30)

	// Act
	for i := 0; i < 500; i++ {
		if err := store.Record(fmt.Sprintf("t-%d", i), "one", "constraint", 1, "pass"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	store.WaitForPrune()
	if err := store.Prune(150, 30); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Assert
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assertion_history WHERE assertion_id = 'one'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 150 {
		t.Fatalf("rows = %d, want 150", n)
	}
}
