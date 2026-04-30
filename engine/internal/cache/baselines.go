package cache

import (
	"database/sql"
	"fmt"
	"time"
)

// BaselineEntry is a single assertion result captured under a baseline tag.
// Baselines are immutable, named snapshots used by `attest report --vs <tag>`
// to compute regression deltas (pass-rate, cost, latency).
type BaselineEntry struct {
	Tag         string
	AssertionID string
	Type        string
	Status      string
	Score       float64
	Cost        float64
	DurationMS  int64
	PinnedAt    time.Time
}

// BaselineSummary aggregates the rows of a single baseline tag for listing.
type BaselineSummary struct {
	Tag             string
	AssertionCount  int
	Passed          int
	SoftFailed      int
	HardFailed      int
	TotalCost       float64
	TotalDurationMS int64
	PinnedAt        time.Time
}

// BaselineStore persists named, immutable snapshots of assertion results
// keyed by a free-form tag (e.g. release name, git SHA). Backed by the same
// SQLite DB as HistoryStore.
type BaselineStore struct {
	db *sql.DB
}

// NewBaselineStore creates the baselines table and index if absent.
func NewBaselineStore(db *sql.DB) (*BaselineStore, error) {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS baselines (
			tag           TEXT    NOT NULL,
			assertion_id  TEXT    NOT NULL,
			assertion_type TEXT   NOT NULL,
			status        TEXT    NOT NULL,
			score         REAL    NOT NULL,
			cost          REAL    NOT NULL DEFAULT 0,
			duration_ms   INTEGER NOT NULL DEFAULT 0,
			pinned_at     INTEGER NOT NULL,
			PRIMARY KEY (tag, assertion_id)
		)
	`); err != nil {
		return nil, fmt.Errorf("create baselines table: %w", err)
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_baselines_tag
		ON baselines (tag, pinned_at)
	`); err != nil {
		return nil, fmt.Errorf("create baselines index: %w", err)
	}
	return &BaselineStore{db: db}, nil
}

// Pin replaces any existing rows for tag with entries. The pinned_at
// timestamp is set to time.Now() for every row in the snapshot. An empty
// tag is rejected so the caller can't accidentally pin the "" baseline.
func (b *BaselineStore) Pin(tag string, entries []BaselineEntry) error {
	if tag == "" {
		return fmt.Errorf("baseline tag must not be empty")
	}
	if len(entries) == 0 {
		return fmt.Errorf("baseline %q has no assertion results to pin", tag)
	}

	tx, err := b.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM baselines WHERE tag = ?`, tag); err != nil {
		return fmt.Errorf("clear existing baseline %q: %w", tag, err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO baselines
		(tag, assertion_id, assertion_type, status, score, cost, duration_ms, pinned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	pinnedAt := time.Now().UnixNano()
	for _, e := range entries {
		if _, err := stmt.Exec(
			tag,
			e.AssertionID,
			e.Type,
			e.Status,
			e.Score,
			e.Cost,
			e.DurationMS,
			pinnedAt,
		); err != nil {
			return fmt.Errorf("insert entry %s: %w", e.AssertionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit baseline %q: %w", tag, err)
	}
	return nil
}

// Get returns all entries for tag, sorted by assertion_id. An unknown tag
// returns an empty slice with nil error so callers can check len(entries)==0.
func (b *BaselineStore) Get(tag string) ([]BaselineEntry, error) {
	rows, err := b.db.Query(`
		SELECT tag, assertion_id, assertion_type, status, score, cost, duration_ms, pinned_at
		FROM baselines
		WHERE tag = ?
		ORDER BY assertion_id
	`, tag)
	if err != nil {
		return nil, fmt.Errorf("query baseline %q: %w", tag, err)
	}
	defer rows.Close()

	var out []BaselineEntry
	for rows.Next() {
		var e BaselineEntry
		var pinnedAtNanos int64
		if err := rows.Scan(
			&e.Tag,
			&e.AssertionID,
			&e.Type,
			&e.Status,
			&e.Score,
			&e.Cost,
			&e.DurationMS,
			&pinnedAtNanos,
		); err != nil {
			return nil, fmt.Errorf("scan baseline row: %w", err)
		}
		e.PinnedAt = time.Unix(0, pinnedAtNanos).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate baseline rows: %w", err)
	}
	return out, nil
}

// List returns one BaselineSummary per pinned tag, ordered by pinned_at DESC.
func (b *BaselineStore) List() ([]BaselineSummary, error) {
	rows, err := b.db.Query(`
		SELECT
			tag,
			COUNT(*) AS n,
			SUM(CASE status WHEN 'pass' THEN 1 ELSE 0 END) AS n_pass,
			SUM(CASE status WHEN 'soft_fail' THEN 1 ELSE 0 END) AS n_soft,
			SUM(CASE status WHEN 'hard_fail' THEN 1 ELSE 0 END) AS n_hard,
			SUM(cost) AS total_cost,
			SUM(duration_ms) AS total_dur,
			MAX(pinned_at) AS latest
		FROM baselines
		GROUP BY tag
		ORDER BY latest DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list baselines: %w", err)
	}
	defer rows.Close()

	var out []BaselineSummary
	for rows.Next() {
		var s BaselineSummary
		var latestNanos int64
		if err := rows.Scan(
			&s.Tag,
			&s.AssertionCount,
			&s.Passed,
			&s.SoftFailed,
			&s.HardFailed,
			&s.TotalCost,
			&s.TotalDurationMS,
			&latestNanos,
		); err != nil {
			return nil, fmt.Errorf("scan baseline summary: %w", err)
		}
		s.PinnedAt = time.Unix(0, latestNanos).UTC()
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate baseline summaries: %w", err)
	}
	return out, nil
}

// Delete removes every row for tag. Returns the number of rows removed so
// callers can distinguish "deleted N" from "no such baseline".
func (b *BaselineStore) Delete(tag string) (int, error) {
	if tag == "" {
		return 0, fmt.Errorf("baseline tag must not be empty")
	}
	res, err := b.db.Exec(`DELETE FROM baselines WHERE tag = ?`, tag)
	if err != nil {
		return 0, fmt.Errorf("delete baseline %q: %w", tag, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return int(n), nil
}
