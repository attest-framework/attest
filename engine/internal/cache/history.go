package cache

import (
	"database/sql"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// HistoryStore is a SQLite-backed store for assertion result history.
type HistoryStore struct {
	db           *sql.DB
	insertCount  atomic.Int64
	pruneMaxRows int
	pruneMaxDays int
}

// NewHistoryStore creates the assertion_history table and index if they don't exist,
// then returns a HistoryStore backed by the provided *sql.DB.
func NewHistoryStore(db *sql.DB) (*HistoryStore, error) {
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

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

// Record inserts a single assertion result row into assertion_history.
// Every 100th insert triggers a background prune using the configured limits.
func (h *HistoryStore) Record(traceID, assertionID, assertionType string, score float64, status string) error {
	_, err := h.db.Exec(
		`INSERT INTO assertion_history (trace_id, assertion_id, assertion_type, score, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		traceID, assertionID, assertionType, score, status, time.Now().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("record assertion history: %w", err)
	}

	n := h.insertCount.Add(1)
	if n%100 == 0 {
		// Non-fatal: prune errors are logged by callers if needed.
		_ = h.Prune(h.pruneMaxRows, h.pruneMaxDays)
	}

	return nil
}

// Prune removes stale and excess rows from assertion_history.
// It deletes rows older than maxAgeDays and, per assertion_id, keeps only the
// maxRows most recent rows.
func (h *HistoryStore) Prune(maxRows int, maxAgeDays int) error {
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).UnixNano()
	if _, err := h.db.Exec(
		`DELETE FROM assertion_history WHERE created_at < ?`,
		cutoff,
	); err != nil {
		return fmt.Errorf("prune by age: %w", err)
	}

	// Per assertion_id, delete rows not in the most-recent maxRows set.
	if _, err := h.db.Exec(
		`DELETE FROM assertion_history
		 WHERE id NOT IN (
		   SELECT id FROM assertion_history a2
		   WHERE a2.assertion_id = assertion_history.assertion_id
		   ORDER BY a2.created_at DESC
		   LIMIT ?
		 )`,
		maxRows,
	); err != nil {
		return fmt.Errorf("prune by row count: %w", err)
	}

	return nil
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
