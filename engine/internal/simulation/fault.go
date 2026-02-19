package simulation

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// FaultConfig defines the fault injection parameters for a FaultInjector.
type FaultConfig struct {
	ErrorRate         float64       // Probability [0,1] of returning an error
	LatencyJitter     time.Duration // Random additional latency [0, LatencyJitter)
	ContentCorruption bool          // If true, randomly corrupts response content
	TimeoutAfter      time.Duration // If > 0, returns context.DeadlineExceeded after this duration
}

// FaultInjector wraps an llm.Provider and injects configurable faults.
type FaultInjector struct {
	inner  llm.Provider
	config FaultConfig
	rng    *rand.Rand
	mu     sync.Mutex
}

// NewFaultInjector creates a FaultInjector with a time-based seed.
func NewFaultInjector(inner llm.Provider, config FaultConfig) *FaultInjector {
	return NewFaultInjectorWithSeed(inner, config, time.Now().UnixNano())
}

// NewFaultInjectorWithSeed creates a FaultInjector with a deterministic seed for testing.
func NewFaultInjectorWithSeed(inner llm.Provider, config FaultConfig, seed int64) *FaultInjector {
	return &FaultInjector{
		inner:  inner,
		config: config,
		rng:    rand.New(rand.NewSource(seed)), //nolint:gosec
	}
}

// Name returns the provider name prefixed with "fault:".
func (f *FaultInjector) Name() string {
	return "fault:" + f.inner.Name()
}

// DefaultModel delegates to the inner provider.
func (f *FaultInjector) DefaultModel() string {
	return f.inner.DefaultModel()
}

// Complete injects faults according to FaultConfig before delegating to the inner provider.
func (f *FaultInjector) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	f.mu.Lock()
	errorRoll := f.rng.Float64()
	var jitter time.Duration
	if f.config.LatencyJitter > 0 {
		jitter = time.Duration(f.rng.Int63n(int64(f.config.LatencyJitter)))
	}
	f.mu.Unlock()

	// 1. ErrorRate check
	if f.config.ErrorRate > 0 && errorRoll < f.config.ErrorRate {
		return nil, fmt.Errorf("injected fault: simulated error")
	}

	// 2. TimeoutAfter check
	if f.config.TimeoutAfter > 0 {
		time.Sleep(f.config.TimeoutAfter)
		return nil, context.DeadlineExceeded
	}

	// 3. LatencyJitter
	if jitter > 0 {
		time.Sleep(jitter)
	}

	// 4. Delegate to inner provider
	resp, err := f.inner.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	// 5. ContentCorruption
	if f.config.ContentCorruption && resp != nil && len(resp.Content) > 0 {
		resp.Content = f.corruptContent(resp.Content)
	}

	return resp, nil
}

// corruptContent randomly shuffles characters in the content string.
func (f *FaultInjector) corruptContent(content string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	chars := []rune(content)
	// Swap a random subset of adjacent pairs
	for i := 0; i < len(chars)-1; i++ {
		if f.rng.Float64() < 0.3 {
			chars[i], chars[i+1] = chars[i+1], chars[i]
		}
	}
	return string(chars)
}
