package main_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/attest-ai/attest/engine/internal/cache"
)

// `baseline pin` while another engine holds the write lock must wait on the
// busy timeout instead of failing with an immediate SQLITE_BUSY.
func TestBaseline_PinWhileAnotherHandleWrites_WaitsAndSucceeds(t *testing.T) {
	// Arrange
	bin := buildEngineBinary(t)
	cacheDir := t.TempDir()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(minimalReportV2(2, 1)), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", "seed", "--report", reportPath); code != 0 {
		t.Fatalf("seed pin exited %d: %s", code, stderr)
	}
	db, release, err := cache.Open(filepath.Join(cacheDir, "attest.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer release()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin write transaction: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE lock_probe (x INTEGER)`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(time.Second)
		_ = tx.Commit()
		close(released)
	}()

	// Act
	stdout, stderr, code := runEngine(t, bin, cacheDir, "baseline", "pin", "--tag", "while-busy", "--report", reportPath)
	<-released

	// Assert
	if code != 0 {
		t.Fatalf("pin exited %d while another handle wrote\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}
