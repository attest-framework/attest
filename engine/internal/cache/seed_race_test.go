//go:build race

package cache_test

import "time"

// seedPerID is smaller under the race detector, which slows SQLite inserts
// ~20x; the instrumented full-table scan of the old prune is slowed by the
// same factor, so the table still holds the lock long enough to be measured.
const seedPerID = 500

// maxRecordWait bounds a Record that collides with a prune; the busy timeout is 5 s.
// Under the race detector one prune batch can hold the lock for 1-2.4 s on a
// shared CI runner, so this build only rules out a writer that waits close to
// the busy timeout. The 500 ms bound of the non-race build is the tight check.
const maxRecordWait = 4 * time.Second
