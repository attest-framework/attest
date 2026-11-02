package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRateLimiter_BudgetExhausted_IsNotRetried(t *testing.T) {
	mock := NewMockProvider(nil, []error{fmt.Errorf("session budget: %w", ErrBudgetExhausted)})
	rl, err := NewRateLimitedProvider(mock, RateLimiterConfig{
		RequestsPerMinute: 600, Burst: 10, MaxRetries: 3,
		InitialBackoff: time.Second, MaxBackoff: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = rl.Complete(context.Background(), &CompletionRequest{})

	if !errors.Is(err, ErrBudgetExhausted) || mock.CallCount != 1 {
		t.Fatalf("err = %v after %d calls, want ErrBudgetExhausted after 1", err, mock.CallCount)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("refusal took %v; it must not back off", time.Since(start))
	}
}

func TestJudgePriceFromEnv_BothSet_ReturnsPrice(t *testing.T) {
	t.Setenv("ATTEST_JUDGE_PRICE_IN", "3")
	t.Setenv("ATTEST_JUDGE_PRICE_OUT", "15")

	p, set, err := JudgePriceFromEnv()

	if err != nil || !set || p != (Price{InPer1M: 3, OutPer1M: 15}) {
		t.Fatalf("got %+v set=%v err=%v, want {3 15}", p, set, err)
	}
	if got := p.Cost(1_000_000, 1_000_000); got != 18 {
		t.Errorf("Cost(1M, 1M) = %v, want 18", got)
	}
}
