package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// HistoryStore is a SQLite-backed store for assertion result history.
type HistoryStore struct {
	db           *sql.DB
	insertCount  atomic.Int64
	pruning      atomic.Bool
	pruneWG      sync.WaitGroup
	pruneMaxRows int
	pruneMaxDays int
	dirtyMu      sync.Mutex
	dirty        map[string]struct{}
	fullPruned   bool
}

// NewHistoryStore creates the assertion_history table and index if they don't exist,
// then returns a HistoryStore backed by the provided *sql.DB. Production
// callers pass the handle from Open, which carries WAL and busy_timeout.
func NewHistoryStore(db *sql.DB) (*HistoryStore, error) {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS assertion_history (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			trace_id       TEXT    NOT NULL,
			assertion_id   TEXT    NOT NULL,
			assertion_type TEXT    NOT NULL,
			score          REAL    NOT NULL,
			status         TEXT    NOT NULL,
			created_at     INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("create assertion_history table: %w", err)
	}

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_assertion_history_id_ts
		ON assertion_history (assertion_id, created_at)
	`); err != nil {
		return nil, fmt.Errorf("create assertion_history index: %w", err)
	}

	return &HistoryStore{
		db:           db,
		dirty:        map[string]struct{}{},
		pruneMaxRows: defaultHistoryMaxRows,
		pruneMaxDays: defaultHistoryMaxAgeDays,
	}, nil
}

const (
	defaultHistoryMaxRows    = 10000
	defaultHistoryMaxAgeDays = 30
)

// SetPruneConfig overrides the pruning parameters (maxRows and maxAgeDays).
// Call before the first Record to take effect.
func (h *HistoryStore) SetPruneConfig(maxRows, maxAgeDays int) {
	h.pruneMaxRows = maxRows
	h.pruneMaxDays = maxAgeDays
}

// pruneBatch bounds how many rows one DELETE statement removes, so each write
// lock hold covers at most this many rows and concurrent Record calls from
// this or other engine processes interleave between statements.
const pruneBatch = 250

// pruneYield is the pause between delete batches that lets waiting writers
// take the lock.
const pruneYield = 5 * time.Millisecond

// Record inserts a single assertion result row into assertion_history.
// Every 100th insert starts a background prune of the assertion ids recorded
// since the previous prune; a prune already running absorbs the trigger, and
// Record never waits for the prune to finish. The prune takes the SQLite write
// lock only in short batches, so an insert that collides with one waits at
// most one batch (plus the pruneYield gap), not the whole prune. not_evaluated rows carry no score and are not
// recorded, so outages and simulation runs never skew drift baselines.
func (h *HistoryStore) Record(traceID, assertionID, assertionType string, score float64, status string) error {
	if status == types.StatusNotEvaluated {
		return nil
	}
	_, err := h.db.Exec(
		`INSERT INTO assertion_history (trace_id, assertion_id, assertion_type, score, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		traceID, assertionID, assertionType, score, status, time.Now().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("record assertion history: %w", err)
	}

	h.dirtyMu.Lock()
	h.dirty[assertionID] = struct{}{}
	h.dirtyMu.Unlock()

	n := h.insertCount.Add(1)
	if n%100 == 0 && h.pruning.CompareAndSwap(false, true) {
		h.pruneWG.Add(1)
		go func() {
			defer h.pruneWG.Done()
			defer h.pruning.Store(false)
			if err := h.pruneDirty(); err != nil {
				slog.Error("history prune failed", "err", err)
			}
		}()
	}

	return nil
}

// WaitForPrune blocks until any background prune has finished. Callers that
// close the database or assert on row counts use it; Record never does.
func (h *HistoryStore) WaitForPrune() {
	h.pruneWG.Wait()
}

// pruneDirty prunes the assertion ids recorded since the last prune. The
// first background prune covers every id in the table so rows of ids this
// process never records still age out.
func (h *HistoryStore) pruneDirty() error {
	h.dirtyMu.Lock()
	ids := make([]string, 0, len(h.dirty))
	for id := range h.dirty {
		ids = append(ids, id)
	}
	h.dirty = map[string]struct{}{}
	full := !h.fullPruned
	h.fullPruned = true
	h.dirtyMu.Unlock()

	if full {
		all, err := h.assertionIDs()
		if err != nil {
			return err
		}
		ids = all
	}
	cutoff := time.Now().AddDate(0, 0, -h.pruneMaxDays).UnixNano()
	for _, id := range ids {
		if err := h.pruneAssertion(id, h.pruneMaxRows, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// Prune removes stale and excess rows from assertion_history for every
// assertion id: rows older than maxAgeDays, then rows past the maxRows most
// recent per id. It runs one id at a time in batches of pruneBatch rows, so no
// statement holds the write lock for longer than one small batch.
func (h *HistoryStore) Prune(maxRows int, maxAgeDays int) error {
	ids, err := h.assertionIDs()
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).UnixNano()
	for _, id := range ids {
		if err := h.pruneAssertion(id, maxRows, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// assertionIDs lists the distinct assertion ids with a read-only query; it
// takes no write lock.
func (h *HistoryStore) assertionIDs() ([]string, error) {
	rows, err := h.db.Query(`SELECT DISTINCT assertion_id FROM assertion_history`)
	if err != nil {
		return nil, fmt.Errorf("list assertion ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan assertion id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list assertion ids rows: %w", err)
	}
	return ids, nil
}

// pruneAssertion deletes one id's rows older than cutoff and rows ranked past
// the maxRows most recent. Both deletes select their victims through the
// (assertion_id, created_at) index and run in pruneBatch-row statements. The
// row-count boundary is found with a read-only query first: skipping maxRows
// index entries inside the DELETE would hold the write lock for that walk.
func (h *HistoryStore) pruneAssertion(id string, maxRows int, cutoff int64) error {
	if maxRows < 1 {
		return fmt.Errorf("prune %q: maxRows must be at least 1, got %d", id, maxRows)
	}
	if err := h.deleteBatched(
		`DELETE FROM assertion_history WHERE id IN (
		   SELECT id FROM assertion_history
		   WHERE assertion_id = ? AND created_at < ? LIMIT ?)`,
		id, cutoff, pruneBatch,
	); err != nil {
		return fmt.Errorf("prune %q by age: %w", id, err)
	}

	// The oldest row inside the keep window; every row ordered before it
	// (created_at, id) ranks past maxRows. New inserts are newer, so the
	// boundary stays valid while the batches run.
	var keepTS, keepID int64
	err := h.db.QueryRow(
		`SELECT created_at, id FROM assertion_history
		 WHERE assertion_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT 1 OFFSET ?`,
		id, maxRows-1,
	).Scan(&keepTS, &keepID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prune %q by row count: find boundary: %w", id, err)
	}
	if err := h.deleteBatched(
		`DELETE FROM assertion_history WHERE id IN (
		   SELECT id FROM assertion_history
		   WHERE assertion_id = ? AND (created_at, id) < (?, ?) LIMIT ?)`,
		id, keepTS, keepID, pruneBatch,
	); err != nil {
		return fmt.Errorf("prune %q by row count: %w", id, err)
	}
	return nil
}

// deleteBatched repeats a DELETE capped at pruneBatch rows until a statement
// removes fewer than pruneBatch.
func (h *HistoryStore) deleteBatched(query string, args ...any) error {
	for {
		res, err := h.db.Exec(query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n < pruneBatch {
			return nil
		}
		// SQLite's busy handler retries with sleeps and has no fairness, so
		// back-to-back batches could starve a waiting writer. Yield the lock.
		time.Sleep(pruneYield)
	}
}

// QueryWindow returns the last windowSize scores for the given assertionID,
// ordered by created_at DESC (most recent first).
func (h *HistoryStore) QueryWindow(assertionID string, windowSize int) ([]float64, error) {
	rows, err := h.db.Query(
		`SELECT score FROM assertion_history
		 WHERE assertion_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		assertionID, windowSize,
	)
	if err != nil {
		return nil, fmt.Errorf("query window: %w", err)
	}
	defer rows.Close()

	var scores []float64
	for rows.Next() {
		var s float64
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan score: %w", err)
		}
		scores = append(scores, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query window rows: %w", err)
	}
	return scores, nil
}

// Stats computes the mean, population standard deviation, and count of all scores
// for the given assertionID. Returns zero values when no rows exist.
// Uses a single query with the statistical identity: stddev = sqrt(avg(x^2) - avg(x)^2).
func (h *HistoryStore) Stats(assertionID string) (mean float64, stddev float64, count int, err error) {
	row := h.db.QueryRow(
		`SELECT COUNT(*), COALESCE(AVG(score), 0.0), COALESCE(AVG(score * score), 0.0) FROM assertion_history WHERE assertion_id = ?`,
		assertionID,
	)
	var avgSq float64
	if err = row.Scan(&count, &mean, &avgSq); err != nil {
		return 0, 0, 0, fmt.Errorf("stats query: %w", err)
	}
	if count == 0 {
		return 0, 0, 0, nil
	}

	// Population stddev via statistical identity: Var(X) = E[X^2] - E[X]^2
	variance := avgSq - mean*mean
	if variance < 0 {
		variance = 0 // guard against floating-point rounding
	}
	stddev = math.Sqrt(variance)
	return mean, stddev, count, nil
}
