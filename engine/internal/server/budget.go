package server

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/llm"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// budgetConfig is the session model-call budget resolved from the
// environment: ATTEST_MAX_JUDGE_CALLS, ATTEST_BUDGET_USD and the judge
// price override ATTEST_JUDGE_PRICE_IN / ATTEST_JUDGE_PRICE_OUT.
type budgetConfig struct {
	limits        assertion.BudgetLimits
	priceOverride *llm.Price
}

// resolveBudgetConfig reads the budget variables. Unset or empty variables
// mean no limit; malformed or negative values are an error.
func resolveBudgetConfig() (budgetConfig, error) {
	var cfg budgetConfig
	if raw := os.Getenv("ATTEST_MAX_JUDGE_CALLS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("ATTEST_MAX_JUDGE_CALLS=%q must be an integer >= 0", raw)
		}
		cfg.limits.MaxJudgeCalls = &n
	}
	if raw := os.Getenv("ATTEST_BUDGET_USD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return cfg, fmt.Errorf("ATTEST_BUDGET_USD=%q must be a finite number >= 0", raw)
		}
		cfg.limits.MaxCostUSD = &v
	}
	price, set, err := llm.JudgePriceFromEnv()
	if err != nil {
		return cfg, err
	}
	if set {
		cfg.priceOverride = &price
	}
	return cfg, nil
}

// checkBudgetPricing fails startup when a USD budget cannot be enforced:
// the judge model has no price, or the judge is the ACP agent, which
// reports no token usage. judgeModel is the model judge calls default to.
func checkBudgetPricing(cfg budgetConfig, judgeProvider, judgeModel string, embedder *assertion.BudgetedEmbedder) error {
	if cfg.limits.MaxCostUSD == nil {
		return nil
	}
	switch {
	case judgeProvider == "acp":
		return fmt.Errorf("ATTEST_BUDGET_USD cannot be enforced for ATTEST_JUDGE_PROVIDER=acp: " +
			"the agent reports no token usage; limit it with ATTEST_MAX_JUDGE_CALLS")
	case judgeProvider != "" && cfg.priceOverride == nil:
		if _, ok := llm.LookupPrice(judgeModel); !ok {
			return fmt.Errorf("ATTEST_BUDGET_USD is set but judge model %q has no built-in price: "+
				"set ATTEST_JUDGE_PRICE_IN and ATTEST_JUDGE_PRICE_OUT (USD per 1M tokens)", judgeModel)
		}
	}
	if embedder != nil {
		if _, ok := llm.LookupPrice(embedder.Model()); !ok {
			return fmt.Errorf("ATTEST_BUDGET_USD is set but embedding model %q has no built-in price", embedder.Model())
		}
	}
	return nil
}

// validateBatchBudget rejects a negative per-batch limit.
func validateBatchBudget(b *types.BatchBudget) *types.RPCError {
	if b == nil {
		return nil
	}
	if (b.MaxJudgeCalls != nil && *b.MaxJudgeCalls < 0) || (b.MaxCostUSD != nil && *b.MaxCostUSD < 0) {
		return types.NewRPCError(
			types.ErrAssertionError,
			"evaluate_batch budget limits must be >= 0",
			types.ErrTypeAssertionError,
			false,
			"send budget.max_judge_calls and budget.max_cost_usd as non-negative numbers",
		)
	}
	return nil
}
