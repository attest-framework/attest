package cache_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/attest-framework/attest/engine/internal/cache"
)

const (
	writerDBEnv   = "ATTEST_TEST_HISTORY_WRITER_DB"
	writerIDEnv   = "ATTEST_TEST_HISTORY_WRITER_ID"
	writerProcs   = 8
	writerRecords = 300
	// The table is pre-seeded so each writer's background prune has a large
	// table to work through while the other seven keep inserting.
	seedIDs = 100
)

// TestHistoryWriterProcess is the child half of
// TestHistoryStore_ParallelEngineProcesses_LoseNoRows: it opens the history DB
// through the production path and records writerRecords rows. It is a no-op
// outside the parent's re-exec.
func TestHistoryWriterProcess(t *testing.T) {
	path := os.Getenv(writerDBEnv)
	if path == "" {
		t.Skip("child process of TestHistoryStore_ParallelEngineProcesses_LoseNoRows")
	}
	id := os.Getenv(writerIDEnv)
	db, release, err := cache.Open(path)
	if err != nil {
		t.Fatalf("writer %s: Open: %v", id, err)
	}
	defer release()
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("writer %s: NewHistoryStore: %v", id, err)
	}
	for i := 0; i < writerRecords; i++ {
		if err := store.Record(fmt.Sprintf("trace-%s-%d", id, i), "shared-assertion", "constraint", 0.5, "pass"); err != nil {
			t.Fatalf("writer %s: Record %d: %v", id, i, err)
		}
	}
	store.WaitForPrune()
}

// Eight engine processes writing one pre-seeded attest.db, each pruning in
// the background, must lose no rows: F9 saw 752 of 800 writes fail with
// SQLITE_BUSY.
func TestHistoryStore_ParallelEngineProcesses_LoseNoRows(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "attest.db")
	seedDB, seedRelease, err := cache.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := cache.NewHistoryStore(seedDB); err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	seedHistory(t, seedDB, seedIDs, seedPerID)
	if err := seedRelease(); err != nil {
		t.Fatalf("release seed handle: %v", err)
	}

	// Act
	var wg sync.WaitGroup
	outputs := make([]string, writerProcs)
	errs := make([]error, writerProcs)
	for i := 0; i < writerProcs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryWriterProcess$")
			cmd.Env = append(os.Environ(), writerDBEnv+"="+path, writerIDEnv+"="+strconv.Itoa(i))
			out, err := cmd.CombinedOutput()
			outputs[i], errs[i] = string(out), err
		}(i)
	}
	wg.Wait()

	// Assert
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("writer %d failed: %v\n%s", i, errs[i], outputs[i])
		}
	}
	db, release, err := cache.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer release()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assertion_history WHERE assertion_id = 'shared-assertion'`).Scan(&got); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if want := writerProcs * writerRecords; got != want {
		t.Fatalf("history rows = %d, want %d", got, want)
	}
}
