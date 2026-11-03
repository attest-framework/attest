package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// JudgeCacheEntry holds a cached LLM judge result.
type JudgeCacheEntry struct {
	Score       float64
	Explanation string
}

// JudgeCache is an LRU-evicting SQLite-backed cache for LLM judge results.
// Entries are scoped to one judge endpoint, so two endpoints serving the same
// model name never share scores.
type JudgeCache struct {
	db       *sql.DB
	maxMB    int
	endpoint string
}

// NewJudgeCache opens (or creates) a judge cache at dbPath for the judge
// endpoint (a normalized base URL). maxMB sets the maximum size in megabytes
// before LRU eviction triggers.
func NewJudgeCache(dbPath string, maxMB int, endpoint string) (*JudgeCache, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("judge cache: endpoint is required")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS judge_cache (
			content_hash TEXT NOT NULL,
			rubric       TEXT NOT NULL,
			model        TEXT NOT NULL,
			score        REAL NOT NULL,
			explanation  TEXT NOT NULL,
			created_at   INTEGER NOT NULL,
			accessed_at  INTEGER NOT NULL,
			PRIMARY KEY (content_hash, rubric, model)
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_judge_accessed ON judge_cache(accessed_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create index: %w", err)
	}

	return &JudgeCache{db: db, maxMB: maxMB, endpoint: endpoint}, nil
}

// JudgePromptKey returns the SHA-256 hex digest of everything that shapes
// a judge prompt besides rubric name and model (which are separate key
// columns): the rubric version and the user message, which carries the
// criteria and the agent output. Two assertions on one output with
// different criteria therefore never share a cached score.
func JudgePromptKey(rubricVersion, userContent string) string {
	sum := sha256.Sum256([]byte(rubricVersion + "\x00" + userContent))
	return hex.EncodeToString(sum[:])
}

// modelKey is the stored model column: the endpoint and the model name. A
// space cannot occur in a normalized URL, so the pair is unambiguous.
func (c *JudgeCache) modelKey(model string) string {
	return c.endpoint + " " + model
}

// Get retrieves a cached judge result for the given content, rubric, and model
// at this cache's endpoint. Returns (nil, nil) on cache miss.
func (c *JudgeCache) Get(contentHash, rubric, model string) (*JudgeCacheEntry, error) {
	model = c.modelKey(model)
	row := c.db.QueryRow(
		`SELECT score, explanation FROM judge_cache WHERE content_hash = ? AND rubric = ? AND model = ?`,
		contentHash, rubric, model,
	)

	var entry JudgeCacheEntry
	if err := row.Scan(&entry.Score, &entry.Explanation); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get judge result: %w", err)
	}

	// Update LRU timestamp
	_, _ = c.db.Exec(
		`UPDATE judge_cache SET accessed_at = ? WHERE content_hash = ? AND rubric = ? AND model = ?`,
		time.Now().UnixNano(), contentHash, rubric, model,
	)

	return &entry, nil
}

// Put stores a judge result, then evicts if over size limit.
func (c *JudgeCache) Put(contentHash, rubric, model string, entry *JudgeCacheEntry) error {
	model = c.modelKey(model)
	now := time.Now().UnixNano()

	_, err := c.db.Exec(
		`INSERT INTO judge_cache(content_hash, rubric, model, score, explanation, created_at, accessed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(content_hash, rubric, model) DO UPDATE SET score=excluded.score, explanation=excluded.explanation, accessed_at=excluded.accessed_at`,
		contentHash, rubric, model, entry.Score, entry.Explanation, now, now,
	)
	if err != nil {
		return fmt.Errorf("put judge result: %w", err)
	}

	return c.evictIfNeeded()
}

// Stats returns current cache statistics.
func (c *JudgeCache) Stats() (*CacheStats, error) {
	row := c.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(explanation)), 0) FROM judge_cache`)
	var stats CacheStats
	if err := row.Scan(&stats.Entries, &stats.TotalBytes); err != nil {
		return nil, fmt.Errorf("judge cache stats: %w", err)
	}
	return &stats, nil
}

// Clear removes all cached entries.
func (c *JudgeCache) Clear() error {
	if _, err := c.db.Exec(`DELETE FROM judge_cache`); err != nil {
		return fmt.Errorf("clear judge cache: %w", err)
	}
	return nil
}

// Close releases the database connection.
func (c *JudgeCache) Close() error {
	return c.db.Close()
}

func (c *JudgeCache) evictIfNeeded() error {
	maxBytes := int64(c.maxMB) * 1024 * 1024

	row := c.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(explanation) + 100), 0) FROM judge_cache`)
	var totalBytes int64
	if err := row.Scan(&totalBytes); err != nil {
		return fmt.Errorf("evict size check: %w", err)
	}

	if totalBytes <= maxBytes {
		return nil
	}

	rows, err := c.db.Query(
		`SELECT content_hash, rubric, model, LENGTH(explanation) + 100 FROM judge_cache ORDER BY accessed_at ASC`,
	)
	if err != nil {
		return fmt.Errorf("evict query: %w", err)
	}
	defer rows.Close()

	type entry struct {
		hash   string
		rubric string
		model  string
		size   int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.hash, &e.rubric, &e.model, &e.size); err != nil {
			return fmt.Errorf("evict scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("evict rows: %w", err)
	}

	for _, e := range entries {
		if totalBytes <= maxBytes {
			break
		}
		if _, err := c.db.Exec(
			`DELETE FROM judge_cache WHERE content_hash = ? AND rubric = ? AND model = ?`,
			e.hash, e.rubric, e.model,
		); err != nil {
			return fmt.Errorf("evict delete: %w", err)
		}
		totalBytes -= e.size
	}

	return nil
}
