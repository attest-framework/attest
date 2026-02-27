package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// lruFlushInterval is how often deferred LRU writes are flushed to SQLite.
	lruFlushInterval = 5 * time.Second
	// lruFlushThreshold triggers a flush when the pending map reaches this size.
	lruFlushThreshold = 64
)

// lruKey is the composite key for deferred LRU writes.
type lruKey struct {
	contentHash string
	model       string
}

// EmbeddingCache is an LRU-evicting SQLite-backed cache for embedding vectors.
type EmbeddingCache struct {
	db    *sql.DB
	maxMB int

	// Deferred LRU writes: buffer accessed_at updates and flush periodically.
	pendingLRU sync.Map    // map[lruKey]int64 (UnixNano)
	pendingLen atomic.Int64
	stopFlush  chan struct{}
	flushDone  chan struct{}
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

	c := &EmbeddingCache{
		db:        db,
		maxMB:     maxMB,
		stopFlush: make(chan struct{}),
		flushDone: make(chan struct{}),
	}

	go c.flushLoop()

	return c, nil
}

// flushLoop periodically writes buffered accessed_at updates to SQLite.
func (c *EmbeddingCache) flushLoop() {
	defer close(c.flushDone)
	ticker := time.NewTicker(lruFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.FlushLRU()
		case <-c.stopFlush:
			c.FlushLRU()
			return
		}
	}
}

// FlushLRU writes all pending accessed_at updates to SQLite in a single transaction.
func (c *EmbeddingCache) FlushLRU() {
	if c.pendingLen.Load() == 0 {
		return
	}

	// Collect and clear pending entries.
	type entry struct {
		key lruKey
		ts  int64
	}
	var entries []entry
	c.pendingLRU.Range(func(k, v any) bool {
		entries = append(entries, entry{key: k.(lruKey), ts: v.(int64)})
		c.pendingLRU.Delete(k)
		return true
	})
	c.pendingLen.Store(0)

	if len(entries) == 0 {
		return
	}

	tx, err := c.db.Begin()
	if err != nil {
		return
	}

	stmt, err := tx.Prepare(`UPDATE embeddings SET accessed_at = ? WHERE content_hash = ? AND model = ?`)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, e := range entries {
		_, _ = stmt.Exec(e.ts, e.key.contentHash, e.key.model)
	}

	_ = tx.Commit()
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

	// Buffer accessed_at update instead of writing to SQLite on every Get.
	key := lruKey{contentHash: contentHash, model: model}
	c.pendingLRU.Store(key, time.Now().UnixNano())
	n := c.pendingLen.Add(1)
	if n >= lruFlushThreshold {
		go c.FlushLRU()
	}

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

// Close flushes pending LRU writes, stops the background flush loop,
// and releases the database connection.
func (c *EmbeddingCache) Close() error {
	close(c.stopFlush)
	<-c.flushDone
	return c.db.Close()
}

func (c *EmbeddingCache) evictIfNeeded() error {
	// Flush pending LRU writes before eviction so accessed_at values are current.
	c.FlushLRU()

	maxBytes := int64(c.maxMB) * 1024 * 1024

	row := c.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(vector)), 0) FROM embeddings`)
	var totalCount int64
	var totalBytes int64
	if err := row.Scan(&totalCount, &totalBytes); err != nil {
		return fmt.Errorf("evict size check: %w", err)
	}

	if totalBytes <= maxBytes {
		return nil
	}

	// Estimate how many rows to delete: assume uniform vector size.
	avgSize := totalBytes / totalCount
	excess := totalBytes - maxBytes
	deleteCount := excess / avgSize
	if deleteCount < 1 {
		deleteCount = 1
	}
	// Add 10% headroom to avoid repeated small evictions.
	deleteCount = deleteCount + deleteCount/10
	if deleteCount > totalCount {
		deleteCount = totalCount
	}

	// Pure SQL batch eviction: delete LRU rows without loading into Go.
	_, err := c.db.Exec(
		`DELETE FROM embeddings WHERE rowid IN (SELECT rowid FROM embeddings ORDER BY accessed_at ASC LIMIT ?)`,
		deleteCount,
	)
	if err != nil {
		return fmt.Errorf("evict delete: %w", err)
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
