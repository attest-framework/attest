//go:build race

package cache_test

import "time"

// seedPerID is smaller under the race detector, which slows SQLite inserts
// ~20x; the instrumented full-table scan of the old prune is slowed by the
// same factor, so the table still holds the lock long enough to be measured.
const seedPerID = 500

// maxRecordWait bounds a Record that collides with a prune; the busy timeout is 5 s.
const maxRecordWait = 2 * time.Second
