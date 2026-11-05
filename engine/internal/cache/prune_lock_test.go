package cache_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/attest-framework/attest/engine/internal/cache"
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

func openHistory(t *testing.T) (*sql.DB, *cache.HistoryStore) {
	t.Helper()
	db, release, err := cache.Open(t.TempDir() + "/attest.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = release() })
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	return db, store
}

func countRows(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assertion_history WHERE assertion_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", id, err)
	}
	return n
}

// The background prune step applies the row cap only to ids recorded since the
// previous run; an over-cap id this process never recorded keeps its rows.
func TestHistoryStore_PruneDirty_TrimsOnlyRecordedIDs(t *testing.T) {
	// Arrange
	db, store := openHistory(t)
	store.SetPruneConfig(150, 30)
	seedHistory(t, db, 2, 400) // seed-0 and seed-1, 400 recent rows each
	if err := store.Record("t-live", "seed-0", "constraint", 1, "pass"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Act
	if err := store.PruneDirty(); err != nil {
		t.Fatalf("PruneDirty: %v", err)
	}

	// Assert
	if n := countRows(t, db, "seed-0"); n != 150 {
		t.Fatalf("recorded id rows = %d, want 150", n)
	}
	if n := countRows(t, db, "seed-1"); n != 400 {
		t.Fatalf("unrecorded id rows = %d, want 400 (row cap applies to recorded ids only)", n)
	}
}

// Rows past the max age are removed for every id, including ids never recorded
// by this process, on each prune run rather than only the first.
func TestHistoryStore_PruneDirty_AgesOutRowsOfUnrecordedIDs(t *testing.T) {
	// Arrange
	db, store := openHistory(t)
	store.SetPruneConfig(10000, 30)
	if err := store.PruneDirty(); err != nil { // an earlier run in this process
		t.Fatalf("first PruneDirty: %v", err)
	}
	old := time.Now().AddDate(0, 0, -60).UnixNano()
	if _, err := db.Exec(`
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 1999)
		INSERT INTO assertion_history (trace_id, assertion_id, assertion_type, score, status, created_at)
		SELECT 'old', 'old-' || i, 'constraint', 0.5, 'pass', ? + i FROM n`, old,
	); err != nil {
		t.Fatalf("seed old rows: %v", err)
	}
	seedHistory(t, db, 1, 5) // recent rows of an unrecorded id survive
	for run := 0; run < 2; run++ {
		if err := store.Record("t-live", "live", "constraint", 1, "pass"); err != nil {
			t.Fatalf("Record: %v", err)
		}

		// Act
		if err := store.PruneDirty(); err != nil {
			t.Fatalf("PruneDirty run %d: %v", run, err)
		}
	}

	// Assert
	var aged int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assertion_history WHERE trace_id = 'old'`).Scan(&aged); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if aged != 0 {
		t.Fatalf("aged rows left = %d, want 0", aged)
	}
	if n := countRows(t, db, "seed-0"); n != 5 {
		t.Fatalf("recent unrecorded rows = %d, want 5", n)
	}
}
