package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrAuth marks a provider rejection of the credentials (HTTP 401 or 403).
// Retrying cannot fix it, and every retry would be billed against the judge
// call budget, so the rate limiter returns it after one call.
var ErrAuth = errors.New("provider rejected the credentials")

// StatusError is returned for a non-2xx provider response. It never carries
// request headers, so API keys cannot leak through it.
type StatusError struct {
	Provider   string
	StatusCode int
	Message    string
	// RetryAfter is the server's Retry-After hint; zero when absent.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Is reports ErrAuth for 401 and 403.
func (e *StatusError) Is(target error) bool {
	return target == ErrAuth && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}

// ParseRetryAfter reads a Retry-After header value: delay-seconds or an
// HTTP-date. It returns zero for an absent, malformed or past value.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		const maxSecs = int64(24 * time.Hour / time.Second)
		if secs > maxSecs {
			secs = maxSecs
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// retryDecision classifies a failed call. Only transient failures are
// retried: HTTP 408, 429 and 5xx, and network errors. Everything else
// (auth, bad request, unparseable reply, budget refusal) fails at once.
func retryDecision(err error) (retry bool, retryAfter time.Duration) {
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == http.StatusRequestTimeout, se.StatusCode == http.StatusTooManyRequests, se.StatusCode >= 500:
			return true, se.RetryAfter
		default:
			return false, 0
		}
	}
	if errors.Is(err, ErrBudgetExhausted) {
		return false, 0
	}
	var ne net.Error
	if errors.As(err, &ne) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		// A cancelled caller context is not a network fault.
		return !errors.Is(err, context.Canceled), 0
	}
	return false, 0
}
