package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitedError is returned when a provider's rate-limit budget has been
// exhausted after MaxRetries attempts. Callers can use errors.As to detect
// rate limiting separately from other failures.
type RateLimitedError struct {
	Provider string
	Attempts int
	Cause    error
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited (%s): exhausted %d retries: %v", e.Provider, e.Attempts, e.Cause)
}

func (e *RateLimitedError) Unwrap() error { return e.Cause }

// IsRateLimited reports whether err is or wraps a *RateLimitedError.
func IsRateLimited(err error) bool {
	var rle *RateLimitedError
	return errors.As(err, &rle)
}

// RateLimiterConfig configures the token-bucket rate limiter.
type RateLimiterConfig struct {
	// RequestsPerMinute is the sustained request rate.
	RequestsPerMinute float64
	// Burst is the maximum burst size above the sustained rate.
	Burst int
	// MaxRetries is the number of retry attempts on rate limit errors.
	MaxRetries int
	// InitialBackoff is the starting backoff duration.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
}

// DefaultRateLimiterConfig returns sensible defaults.
var DefaultRateLimiterConfig = RateLimiterConfig{
	RequestsPerMinute: 60,
	Burst:             10,
	MaxRetries:        3,
	InitialBackoff:    500 * time.Millisecond,
	MaxBackoff:        30 * time.Second,
}

// RateLimitedProvider wraps a Provider with token-bucket rate limiting and retry.
type RateLimitedProvider struct {
	inner   Provider
	limiter *rate.Limiter
	cfg     RateLimiterConfig
}

// NewRateLimitedProvider wraps inner with rate limiting using cfg.
func NewRateLimitedProvider(inner Provider, cfg RateLimiterConfig) (*RateLimitedProvider, error) {
	if cfg.RequestsPerMinute <= 0 {
		return nil, fmt.Errorf("rate limiter: RequestsPerMinute must be > 0")
	}
	if cfg.Burst <= 0 {
		return nil, fmt.Errorf("rate limiter: Burst must be > 0")
	}

	perSecond := rate.Limit(cfg.RequestsPerMinute / 60.0)
	limiter := rate.NewLimiter(perSecond, cfg.Burst)

	return &RateLimitedProvider{
		inner:   inner,
		limiter: limiter,
		cfg:     cfg,
	}, nil
}

// Name delegates to the inner provider.
func (r *RateLimitedProvider) Name() string { return r.inner.Name() }

// DefaultModel delegates to the inner provider.
func (r *RateLimitedProvider) DefaultModel() string { return r.inner.DefaultModel() }

// Complete waits for a rate limit token then calls the inner provider.
// On transient failure it retries with exponential backoff up to MaxRetries.
func (r *RateLimitedProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := r.backoff(attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, fmt.Errorf("rate limited provider: context cancelled during backoff: %w", ctx.Err())
			}
		}

		if err := r.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter wait: %w", err)
		}

		resp, err := r.inner.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, &RateLimitedError{
		Provider: r.inner.Name(),
		Attempts: r.cfg.MaxRetries + 1,
		Cause:    lastErr,
	}
}

// backoff returns the exponential backoff duration for the given attempt (1-based).
func (r *RateLimitedProvider) backoff(attempt int) time.Duration {
	d := float64(r.cfg.InitialBackoff) * math.Pow(2, float64(attempt-1))
	if d > float64(r.cfg.MaxBackoff) {
		d = float64(r.cfg.MaxBackoff)
	}
	return time.Duration(d)
}
