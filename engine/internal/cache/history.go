package cache

import (
	"database/sql"
	"fmt"
	"math"
	"time"

	_ "modernc.org/sqlite"
)

// HistoryStore is a SQLite-backed store for assertion result history.
type HistoryStore struct {
	db *sql.DB
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

	return &HistoryStore{db: db}, nil
}

// Record inserts a single assertion result row into assertion_history.
func (h *HistoryStore) Record(traceID, assertionID, assertionType string, score float64, status string) error {
	_, err := h.db.Exec(
		`INSERT INTO assertion_history (trace_id, assertion_id, assertion_type, score, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		traceID, assertionID, assertionType, score, status, time.Now().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("record assertion history: %w", err)
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
func (h *HistoryStore) Stats(assertionID string) (mean float64, stddev float64, count int, err error) {
	row := h.db.QueryRow(
		`SELECT COUNT(*), COALESCE(AVG(score), 0.0) FROM assertion_history WHERE assertion_id = ?`,
		assertionID,
	)
	if err = row.Scan(&count, &mean); err != nil {
		return 0, 0, 0, fmt.Errorf("stats query: %w", err)
	}
	if count == 0 {
		return 0, 0, 0, nil
	}

	// Compute population stddev manually: SQLite lacks STDDEV_POP.
	rows, err := h.db.Query(
		`SELECT score FROM assertion_history WHERE assertion_id = ?`,
		assertionID,
	)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("stats stddev query: %w", err)
	}
	defer rows.Close()

	var sumSqDiff float64
	for rows.Next() {
		var s float64
		if scanErr := rows.Scan(&s); scanErr != nil {
			return 0, 0, 0, fmt.Errorf("stats scan: %w", scanErr)
		}
		diff := s - mean
		sumSqDiff += diff * diff
	}
	if rowErr := rows.Err(); rowErr != nil {
		return 0, 0, 0, fmt.Errorf("stats rows: %w", rowErr)
	}

	stddev = math.Sqrt(sumSqDiff / float64(count))
	return mean, stddev, count, nil
}
