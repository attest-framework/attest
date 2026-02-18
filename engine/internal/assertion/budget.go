package assertion

import (
	"fmt"
	"sync"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// BudgetExceededError is returned when the soft-fail budget is exhausted.
type BudgetExceededError struct {
	Limit   int
	Current int
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("soft-fail budget exceeded: %d/%d soft failures", e.Current, e.Limit)
}

// BudgetTracker counts soft failures and enforces a maximum.
// It is safe for concurrent use.
type BudgetTracker struct {
	mu          sync.Mutex
	limit       int
	softFails   int
	totalCost   float64
	totalTokens int
}

// NewBudgetTracker creates a tracker with the given maximum number of allowed soft failures.
// A limit of 0 means no soft failures are allowed.
func NewBudgetTracker(limit int) *BudgetTracker {
	return &BudgetTracker{limit: limit}
}

// Record accounts for an assertion result.
// Returns BudgetExceededError if the result is a soft_fail and the limit has been reached.
func (b *BudgetTracker) Record(result *types.AssertionResult) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.totalCost += result.Cost

	if result.Status == types.StatusSoftFail {
		b.softFails++
		if b.softFails > b.limit {
			return &BudgetExceededError{Limit: b.limit, Current: b.softFails}
		}
	}
	return nil
}

// SoftFails returns the current soft-failure count.
func (b *BudgetTracker) SoftFails() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.softFails
}

// TotalCost returns the accumulated cost across all recorded results.
func (b *BudgetTracker) TotalCost() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalCost
}

// Remaining returns how many additional soft failures are allowed.
func (b *BudgetTracker) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.softFails
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Reset clears all counters.
func (b *BudgetTracker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.softFails = 0
	b.totalCost = 0
	b.totalTokens = 0
}
