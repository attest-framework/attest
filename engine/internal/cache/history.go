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

	"github.com/attest-framework/attest/engine/pkg/types"
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

	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_assertion_history_ts
		ON assertion_history (created_at)
	`); err != nil {
		return nil, fmt.Errorf("create assertion_history age index: %w", err)
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

// pruneDirty is one background prune run: the global age delete, then the row
// cap for the assertion ids recorded since the previous run. The age delete
// covers every id in one indexed pass, so a short-lived process that never
// reaches a later id still ages out old rows.
func (h *HistoryStore) pruneDirty() error {
	h.dirtyMu.Lock()
	ids := make([]string, 0, len(h.dirty))
	for id := range h.dirty {
		ids = append(ids, id)
	}
	h.dirty = map[string]struct{}{}
	h.dirtyMu.Unlock()

	if err := h.pruneAge(h.pruneMaxDays); err != nil {
		return err
	}
	for _, id := range ids {
		if err := h.pruneRows(id, h.pruneMaxRows); err != nil {
			return err
		}
	}
	return nil
}

// Prune removes stale and excess rows from assertion_history for every
// assertion id: rows older than maxAgeDays in one global pass, then rows past
// the maxRows most recent per id. Every statement deletes at most pruneBatch
// rows, so none holds the write lock for longer than one small batch.
func (h *HistoryStore) Prune(maxRows int, maxAgeDays int) error {
	ids, err := h.assertionIDs()
	if err != nil {
		return err
	}
	if err := h.pruneAge(maxAgeDays); err != nil {
		return err
	}
	for _, id := range ids {
		if err := h.pruneRows(id, maxRows); err != nil {
			return err
		}
	}
	return nil
}

// pruneAge deletes every row older than maxAgeDays, whatever its assertion id,
// selecting victims through the created_at index in pruneBatch-row statements.
func (h *HistoryStore) pruneAge(maxAgeDays int) error {
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).UnixNano()
	if err := h.deleteBatched(
		`DELETE FROM assertion_history WHERE id IN (
		   SELECT id FROM assertion_history WHERE created_at < ? LIMIT ?)`,
		cutoff, pruneBatch,
	); err != nil {
		return fmt.Errorf("prune by age: %w", err)
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

// pruneRows deletes one id's rows ranked past the maxRows most recent, in
// pruneBatch-row statements through the (assertion_id, created_at) index. The
// row-count boundary is found with a read-only query first: skipping maxRows
// index entries inside the DELETE would hold the write lock for that walk.
func (h *HistoryStore) pruneRows(id string, maxRows int) error {
	if maxRows < 1 {
		return fmt.Errorf("prune %q: maxRows must be at least 1, got %d", id, maxRows)
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
// ordered by insertion id DESC (most recent first), the same order QueryBaseline uses.
func (h *HistoryStore) QueryWindow(assertionID string, windowSize int) ([]float64, error) {
	rows, err := h.db.Query(
		`SELECT score FROM assertion_history
		 WHERE assertion_id = ?
		 ORDER BY id DESC
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

// QueryBaseline returns up to windowSize scores of passing and soft-failing
// runs for assertionID, newest first. Hard failures never enter a baseline, so
// a run of failures cannot become the bar. With excludeLatest the newest row
// of any status is left out, which is the baseline the newest run was judged
// against.
func (h *HistoryStore) QueryBaseline(assertionID string, windowSize int, excludeLatest bool) ([]float64, error) {
	query := `SELECT score FROM assertion_history
		 WHERE assertion_id = ? AND status IN (?, ?)`
	args := []any{assertionID, types.StatusPass, types.StatusSoftFail}
	if excludeLatest {
		query += ` AND id < (SELECT COALESCE(MAX(id), 0) FROM assertion_history WHERE assertion_id = ?)`
		args = append(args, assertionID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, windowSize)

	rows, err := h.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query baseline: %w", err)
	}
	defer rows.Close()

	var scores []float64
	for rows.Next() {
		var s float64
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan baseline score: %w", err)
		}
		scores = append(scores, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query baseline rows: %w", err)
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
