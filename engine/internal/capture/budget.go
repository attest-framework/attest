package capture

import "sync"

// byteBudget is a session's recording allowance, shared by the proxy (request
// and response bodies) and the receiver (decoded OTLP bodies). Past
// MaxSessionBytes the session is over budget and its capture fails.
type byteBudget struct {
	mu   sync.Mutex
	used int64
	over bool
}

// charge adds n bytes and reports whether the session is still within budget.
func (b *byteBudget) charge(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used += int64(n)
	if b.used > MaxSessionBytes {
		b.over = true
	}
	return !b.over
}

func (b *byteBudget) exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.over
}
