package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	_ "modernc.org/sqlite"
)

// EmbeddingCache is an LRU-evicting SQLite-backed cache for embedding vectors.
type EmbeddingCache struct {
	db    *sql.DB
	maxMB int
}

// CacheStats reports current usage of the embedding cache.
type CacheStats struct {
	Entries    int
	TotalBytes int64
}

// NewEmbeddingCache opens (or creates) an embedding cache at dbPath.
// maxMB sets the maximum size in megabytes before LRU eviction triggers.
func NewEmbeddingCache(dbPath string, maxMB int) (*EmbeddingCache, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS embeddings (
			content_hash TEXT NOT NULL,
			model        TEXT NOT NULL,
			vector       BLOB NOT NULL,
			created_at   INTEGER NOT NULL,
			accessed_at  INTEGER NOT NULL,
			PRIMARY KEY (content_hash, model)
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_accessed ON embeddings(accessed_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create index: %w", err)
	}

	return &EmbeddingCache{db: db, maxMB: maxMB}, nil
}

// ContentHash returns the SHA-256 hex digest of the given text.
func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Get retrieves a cached vector for the given content and model.
// Returns (nil, nil) on cache miss.
func (c *EmbeddingCache) Get(contentHash, model string) ([]float32, error) {
	row := c.db.QueryRow(
		`SELECT vector FROM embeddings WHERE content_hash = ? AND model = ?`,
		contentHash, model,
	)

	var blob []byte
	if err := row.Scan(&blob); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get embedding: %w", err)
	}

	// Update accessed_at for LRU tracking
	_, _ = c.db.Exec(
		`UPDATE embeddings SET accessed_at = ? WHERE content_hash = ? AND model = ?`,
		time.Now().UnixNano(), contentHash, model,
	)

	return blobToVector(blob)
}

// Put stores a vector for the given content and model, then evicts if over size limit.
func (c *EmbeddingCache) Put(contentHash, model string, vector []float32) error {
	blob := vectorToBlob(vector)
	now := time.Now().UnixNano()

	_, err := c.db.Exec(
		`INSERT INTO embeddings(content_hash, model, vector, created_at, accessed_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(content_hash, model) DO UPDATE SET vector=excluded.vector, accessed_at=excluded.accessed_at`,
		contentHash, model, blob, now, now,
	)
	if err != nil {
		return fmt.Errorf("put embedding: %w", err)
	}

	return c.evictIfNeeded()
}

// Evict removes the least-recently-used entries until the cache is under maxMB.
func (c *EmbeddingCache) Evict() error {
	return c.evictIfNeeded()
}

// Stats returns current cache statistics.
func (c *EmbeddingCache) Stats() (*CacheStats, error) {
	row := c.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(vector)), 0) FROM embeddings`)
	var stats CacheStats
	if err := row.Scan(&stats.Entries, &stats.TotalBytes); err != nil {
		return nil, fmt.Errorf("stats query: %w", err)
	}
	return &stats, nil
}

// Clear removes all cached entries.
func (c *EmbeddingCache) Clear() error {
	if _, err := c.db.Exec(`DELETE FROM embeddings`); err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}
	return nil
}

// Close releases the database connection.
func (c *EmbeddingCache) Close() error {
	return c.db.Close()
}

func (c *EmbeddingCache) evictIfNeeded() error {
	maxBytes := int64(c.maxMB) * 1024 * 1024

	row := c.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(vector)), 0) FROM embeddings`)
	var totalBytes int64
	if err := row.Scan(&totalBytes); err != nil {
		return fmt.Errorf("evict size check: %w", err)
	}

	if totalBytes <= maxBytes {
		return nil
	}

	// Delete LRU rows until under limit
	rows, err := c.db.Query(
		`SELECT content_hash, LENGTH(vector) FROM embeddings ORDER BY accessed_at ASC`,
	)
	if err != nil {
		return fmt.Errorf("evict query: %w", err)
	}
	defer rows.Close()

	type entry struct {
		hash string
		size int64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.hash, &e.size); err != nil {
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
		if _, err := c.db.Exec(`DELETE FROM embeddings WHERE content_hash = ?`, e.hash); err != nil {
			return fmt.Errorf("evict delete: %w", err)
		}
		totalBytes -= e.size
	}

	return nil
}

// vectorToBlob encodes []float32 as little-endian bytes.
func vectorToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// blobToVector decodes little-endian bytes to []float32.
func blobToVector(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("blob length %d is not a multiple of 4", len(blob))
	}
	v := make([]float32, len(blob)/4)
	for i := range v {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		v[i] = math.Float32frombits(bits)
	}
	return v, nil
}
