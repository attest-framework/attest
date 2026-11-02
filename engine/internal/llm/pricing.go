package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
)

// Price is a model's list price in USD per one million tokens.
type Price struct {
	InPer1M  float64
	OutPer1M float64
}

// Cost returns the USD cost of a call with the given token counts. Cached
// input tokens are charged at the full input price, so the result is an
// upper bound for providers that discount them.
func (p Price) Cost(inputTokens, outputTokens int) float64 {
	return (float64(inputTokens)*p.InPer1M + float64(outputTokens)*p.OutPer1M) / 1_000_000
}

// priceTable lists the models whose list prices the engine knows. Any
// other model needs ATTEST_JUDGE_PRICE_IN / ATTEST_JUDGE_PRICE_OUT before a
// USD budget can be enforced for it.
var priceTable = map[string]Price{
	"gpt-4.1":                {InPer1M: 2.00, OutPer1M: 8.00},
	"gpt-4.1-mini":           {InPer1M: 0.40, OutPer1M: 1.60},
	"gpt-4.1-nano":           {InPer1M: 0.10, OutPer1M: 0.40},
	"text-embedding-3-small": {InPer1M: 0.02},
	"text-embedding-3-large": {InPer1M: 0.13},
}

// LookupPrice returns the built-in list price of model.
func LookupPrice(model string) (Price, bool) {
	p, ok := priceTable[model]
	return p, ok
}

// JudgePriceFromEnv reads ATTEST_JUDGE_PRICE_IN and ATTEST_JUDGE_PRICE_OUT
// (USD per 1M tokens). Both must be set together; set reports whether they
// were. Empty counts as unset. Values must be finite and >= 0.
func JudgePriceFromEnv() (price Price, set bool, err error) {
	in, out := os.Getenv("ATTEST_JUDGE_PRICE_IN"), os.Getenv("ATTEST_JUDGE_PRICE_OUT")
	inSet, outSet := in != "", out != ""
	if !inSet && !outSet {
		return Price{}, false, nil
	}
	if !inSet || !outSet {
		return Price{}, false, fmt.Errorf("ATTEST_JUDGE_PRICE_IN and ATTEST_JUDGE_PRICE_OUT must be set together")
	}
	if price.InPer1M, err = parsePrice("ATTEST_JUDGE_PRICE_IN", in); err != nil {
		return Price{}, false, err
	}
	if price.OutPer1M, err = parsePrice("ATTEST_JUDGE_PRICE_OUT", out); err != nil {
		return Price{}, false, err
	}
	return price, true, nil
}

func parsePrice(name, raw string) (float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number: %w", name, raw, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("%s=%q must be a finite number >= 0 (USD per 1M tokens)", name, raw)
	}
	return v, nil
}

type attemptKey struct{}

// WithAttempt marks ctx with a 0-based call attempt. RateLimitedProvider
// sets it so the budget ledger can attribute the cost of retries.
func WithAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, attemptKey{}, attempt)
}

// AttemptFromContext returns the attempt set by WithAttempt, or 0.
func AttemptFromContext(ctx context.Context) int {
	n, _ := ctx.Value(attemptKey{}).(int)
	return n
}

// ErrBudgetExhausted is returned, wrapped, by a budgeted provider when a
// call cannot reserve its cost or call count. The call is not sent and is
// never retried.
var ErrBudgetExhausted = errors.New("budget exhausted")
