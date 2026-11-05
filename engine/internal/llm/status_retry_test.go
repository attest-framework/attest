package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetryConfig() RateLimiterConfig {
	return RateLimiterConfig{
		RequestsPerMinute: 60000, Burst: 10, MaxRetries: 3,
		InitialBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond,
	}
}

func TestRateLimiter_AuthFailure_IsNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		mock := NewMockProvider(nil, []error{
			&StatusError{Provider: "mock", StatusCode: code, Message: "bad key"},
			&StatusError{Provider: "mock", StatusCode: code, Message: "bad key"},
		})
		rl, err := NewRateLimitedProvider(mock, fastRetryConfig())
		if err != nil {
			t.Fatal(err)
		}

		_, callErr := rl.Complete(context.Background(), &CompletionRequest{})

		if !errors.Is(callErr, ErrAuth) {
			t.Errorf("%d: err = %v, want ErrAuth", code, callErr)
		}
		if got := mock.GetCallCount(); got != 1 {
			t.Errorf("%d: calls = %d, want exactly 1", code, got)
		}
	}
}

func TestRateLimiter_NonTransientStatuses_AreNotRetried(t *testing.T) {
	for _, code := range []int{400, 404, 422} {
		mock := NewMockProvider(nil, []error{&StatusError{Provider: "mock", StatusCode: code}})
		rl, _ := NewRateLimitedProvider(mock, fastRetryConfig())

		_, callErr := rl.Complete(context.Background(), &CompletionRequest{})

		if callErr == nil || mock.GetCallCount() != 1 {
			t.Errorf("%d: err=%v calls=%d, want one call and an error", code, callErr, mock.GetCallCount())
		}
	}
}

func TestRateLimiter_TransientStatuses_AreRetried(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503} {
		mock := NewMockProvider([]*CompletionResponse{{Content: "ok"}}, []error{
			&StatusError{Provider: "mock", StatusCode: code}, nil,
		})
		rl, _ := NewRateLimitedProvider(mock, fastRetryConfig())

		resp, err := rl.Complete(context.Background(), &CompletionRequest{})

		if err != nil || resp.Content != "ok" || mock.GetCallCount() != 2 {
			t.Errorf("%d: err=%v calls=%d, want success on the second call", code, err, mock.GetCallCount())
		}
	}
}

func TestRateLimiter_NonNetworkError_IsNotRetried(t *testing.T) {
	mock := NewMockProvider(nil, []error{errors.New("unmarshal: bad json")})
	rl, _ := NewRateLimitedProvider(mock, fastRetryConfig())

	_, err := rl.Complete(context.Background(), &CompletionRequest{})

	if err == nil || mock.GetCallCount() != 1 {
		t.Errorf("err=%v calls=%d, want one call", err, mock.GetCallCount())
	}
}

func TestRateLimiter_RetryAfter_IsHonoured(t *testing.T) {
	mock := NewMockProvider([]*CompletionResponse{{Content: "ok"}}, []error{
		&StatusError{Provider: "mock", StatusCode: 429, RetryAfter: 120 * time.Millisecond}, nil,
	})
	rl, _ := NewRateLimitedProvider(mock, fastRetryConfig())

	start := time.Now()
	_, err := rl.Complete(context.Background(), &CompletionRequest{})

	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 120*time.Millisecond {
		t.Errorf("retried after %v, want at least the 120ms Retry-After", elapsed)
	}
}

func TestRateLimiter_RetryAfterBeyondDeadline_FailsWithoutSleeping(t *testing.T) {
	mock := NewMockProvider(nil, []error{&StatusError{Provider: "mock", StatusCode: 429, RetryAfter: time.Hour}})
	rl, _ := NewRateLimitedProvider(mock, fastRetryConfig())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	_, err := rl.Complete(ctx, &CompletionRequest{})

	var rle *RateLimitedError
	if !errors.As(err, &rle) || time.Since(start) > 500*time.Millisecond {
		t.Errorf("err=%v after %v, want an immediate RateLimitedError", err, time.Since(start))
	}
}

func TestRateLimiter_Backoff_IsJittered(t *testing.T) {
	rl, _ := NewRateLimitedProvider(NewMockProvider(nil, nil), RateLimiterConfig{
		RequestsPerMinute: 60, Burst: 1, MaxRetries: 3,
		InitialBackoff: 800 * time.Millisecond, MaxBackoff: 10 * time.Second,
	})
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		d := rl.backoff(1)
		if d < 400*time.Millisecond || d > 800*time.Millisecond {
			t.Fatalf("backoff = %v, want within [400ms, 800ms]", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Error("backoff is constant: no jitter")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"0":                             0,
		"-3":                            0,
		"soon":                          0,
		"Thu, 01 Jan 2026 00:00:30 GMT": 30 * time.Second,
		"Wed, 31 Dec 2025 23:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := ParseRetryAfter(in, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOpenAIProvider_Unauthorized_IsStatusErrorWithRedactedMessage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key sk-secret-123"}}`))
	}))
	defer srv.Close()
	p, _ := NewOpenAIProvider("sk-secret-123", "m", srv.URL)

	_, err := p.Complete(context.Background(), &CompletionRequest{Messages: []Message{{Role: "user", Content: "x"}}})

	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 401 || se.RetryAfter != 3*time.Second {
		t.Fatalf("err = %#v, want StatusError 401 with Retry-After 3s", err)
	}
	if !errors.Is(err, ErrAuth) {
		t.Error("401 is not ErrAuth")
	}
	if strings.Contains(err.Error(), "sk-secret-123") {
		t.Errorf("error leaks the key: %v", err)
	}
}
