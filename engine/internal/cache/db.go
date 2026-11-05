package cache

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const (
	// busyTimeoutMS is how long a connection waits for another writer's lock
	// before SQLite reports SQLITE_BUSY. Parallel CI workers each run an engine
	// process on the same attest.db, so a writer must wait, not fail.
	busyTimeoutMS = 5000
	// maxOpenConns bounds the pool. WAL lets readers run beside one writer.
	maxOpenConns = 4
)

// sharedDSN is the production connection string: WAL journaling, a busy
// timeout on every connection, and BEGIN IMMEDIATE so a transaction takes the
// write lock up front instead of failing on a read-to-write upgrade.
func sharedDSN(path string) string {
	return fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path, busyTimeoutMS)
}

type sharedEntry struct {
	db   *sql.DB
	refs int
}

var (
	sharedMu  sync.Mutex
	sharedDBs = map[string]*sharedEntry{}
)

// Open returns the process-wide *sql.DB for the SQLite file at path, opening
// it on first use through the production DSN. Every cache store in one engine
// process shares that handle. The returned release drops one reference and
// closes the handle when the last reference is released.
func Open(path string) (*sql.DB, func() error, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}

	sharedMu.Lock()
	defer sharedMu.Unlock()

	e, ok := sharedDBs[abs]
	if !ok {
		db, err := sql.Open("sqlite", sharedDSN(abs))
		if err != nil {
			return nil, nil, fmt.Errorf("open sqlite: %w", err)
		}
		db.SetMaxOpenConns(maxOpenConns)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("open sqlite %s: %w", abs, err)
		}
		e = &sharedEntry{db: db}
		sharedDBs[abs] = e
	}
	e.refs++

	var once sync.Once
	var closeErr error
	release := func() error {
		once.Do(func() {
			sharedMu.Lock()
			defer sharedMu.Unlock()
			e.refs--
			if e.refs == 0 {
				delete(sharedDBs, abs)
				closeErr = e.db.Close()
			}
		})
		return closeErr
	}
	return e.db, release, nil
}
