//go:build !race

package cache_test

import "time"

// seedPerID sizes the pre-seeded history tables: 100 ids x 5,000 rows is large
// enough that a full-table window scan holds the write lock for hundreds of ms.
const seedPerID = 5000

// maxRecordWait bounds a Record that collides with a prune; the busy timeout is 5 s.
const maxRecordWait = 500 * time.Millisecond
