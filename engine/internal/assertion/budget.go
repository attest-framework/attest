package assertion

import (
	"context"
	"fmt"
	"sync"

	"github.com/attest-framework/attest/engine/internal/assertion/embedding"
	"github.com/attest-framework/attest/engine/internal/llm"
	"github.com/attest-framework/attest/engine/pkg/types"
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

// BudgetLimits caps attest-initiated model calls. A nil field means no
// limit. MaxJudgeCalls counts judge-provider requests (judges, repeats,
// bias probes, simulated-user messages, retries); MaxCostUSD covers those
// plus paid embedding calls.
type BudgetLimits struct {
	MaxJudgeCalls *int
	MaxCostUSD    *float64
}

// Ledger enforces BudgetLimits by reservation: every model call reserves
// one call and its worst-case cost (price × input upper bound + price ×
// output limit) before it is sent, and settles to the reported usage when
// it returns. Concurrent callers can therefore never exceed a limit, and
// the in-flight exposure is the sum of outstanding reservations.
//
// A Ledger always collects usage, so it is installed even without limits.
type Ledger struct {
	mu     sync.Mutex
	limits BudgetLimits
	tally  budgetTally
}

// BudgetScope is a child budget for one evaluate_batch call (a per-test
// budget marker). A call must fit both the scope and the session ledger.
// Its fields are guarded by the owning ledger's mutex.
type BudgetScope struct {
	ledger *Ledger
	limits BudgetLimits
	tally  budgetTally
}

type budgetTally struct {
	calls       int
	reservedUSD float64
	// heldUSD is the reservation of calls that failed after being sent:
	// the provider may have billed them, so they stay charged against the
	// limit although usage reports no cost for them.
	heldUSD float64
	usage   types.Usage
}

// committed is the spend counted against a USD limit.
func (t *budgetTally) committed() float64 {
	return t.usage.CostUSD + t.heldUSD + t.reservedUSD
}

// fits reports whether one more call costing usd fits limits; reason is
// set when it does not.
func (t *budgetTally) fits(l BudgetLimits, judgeCall bool, usd float64, priced bool) (ok bool, reason string) {
	if judgeCall && l.MaxJudgeCalls != nil && t.calls+1 > *l.MaxJudgeCalls {
		return false, fmt.Sprintf("max_judge_calls=%d reached", *l.MaxJudgeCalls)
	}
	if l.MaxCostUSD != nil {
		if !priced {
			return false, "the model has no price, so a USD budget cannot cover it"
		}
		if t.committed()+usd > *l.MaxCostUSD {
			return false, fmt.Sprintf("max_cost_usd=%g cannot cover $%.6f (spent $%.6f, held $%.6f, in flight $%.6f)",
				*l.MaxCostUSD, usd, t.usage.CostUSD, t.heldUSD, t.reservedUSD)
		}
	}
	return true, ""
}

// NewLedger returns a ledger enforcing limits.
func NewLedger(limits BudgetLimits) *Ledger {
	return &Ledger{limits: limits}
}

// Limits returns the session limits.
func (l *Ledger) Limits() BudgetLimits { return l.limits }

// Usage returns the session usage so far.
func (l *Ledger) Usage() types.Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tally.usage
}

// usdLimited reports whether a USD limit applies to a call under scope.
func (l *Ledger) usdLimited(scope *BudgetScope) bool {
	return l.limits.MaxCostUSD != nil || (scope != nil && scope.ledger == l && scope.limits.MaxCostUSD != nil)
}

// NewScope returns a child budget limited by b (nil: no extra limit).
func (l *Ledger) NewScope(b *types.BatchBudget) *BudgetScope {
	s := &BudgetScope{ledger: l}
	if b != nil {
		s.limits = BudgetLimits{MaxJudgeCalls: b.MaxJudgeCalls, MaxCostUSD: b.MaxCostUSD}
	}
	return s
}

// Usage returns the usage recorded under this scope.
func (s *BudgetScope) Usage() types.Usage {
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	return s.tally.usage
}

// reservation is one admitted call awaiting settle.
type reservation struct {
	ledger    *Ledger
	scope     *BudgetScope
	usd       float64
	judgeCall bool
}

// reserve admits one call costing at most usd, or returns an error
// wrapping llm.ErrBudgetExhausted. scope may be nil.
func (l *Ledger) reserve(scope *BudgetScope, judgeCall bool, usd float64, priced bool, model string) (*reservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if scope != nil && scope.ledger != l {
		scope = nil
	}
	ok, why := l.tally.fits(l.limits, judgeCall, usd, priced)
	where := "session budget"
	if ok && scope != nil {
		ok, why = scope.tally.fits(scope.limits, judgeCall, usd, priced)
		where = "test budget"
	}
	if !ok {
		l.tally.usage.BudgetRefused++
		if scope != nil {
			scope.tally.usage.BudgetRefused++
		}
		return nil, fmt.Errorf("%w: %s: %s (model %s)", llm.ErrBudgetExhausted, where, why, model)
	}
	for _, t := range l.tallies(scope) {
		if judgeCall {
			t.calls++
		}
		t.reservedUSD += usd
	}
	return &reservation{ledger: l, scope: scope, usd: usd, judgeCall: judgeCall}, nil
}

func (l *Ledger) tallies(scope *BudgetScope) []*budgetTally {
	if scope == nil {
		return []*budgetTally{&l.tally}
	}
	return []*budgetTally{&l.tally, &scope.tally}
}

// settle releases the reservation and records the call's actual usage.
func (r *reservation) settle(u types.Usage) {
	r.ledger.mu.Lock()
	defer r.ledger.mu.Unlock()
	for _, t := range r.ledger.tallies(r.scope) {
		t.reservedUSD -= r.usd
		t.usage.Add(u)
	}
}

// fail settles a call that returned an error after being sent. It keeps
// its call count and records zero tokens, and its reservation stays held
// against the USD limit because the provider may have billed it.
func (r *reservation) fail(u types.Usage) {
	r.ledger.mu.Lock()
	defer r.ledger.mu.Unlock()
	for _, t := range r.ledger.tallies(r.scope) {
		t.reservedUSD -= r.usd
		t.heldUSD += r.usd
		t.usage.Add(u)
	}
}

// defaultMaxOutputTokens caps a request that sets no output limit, so its
// reservation is bounded; providers omit an unset limit from the request.
const defaultMaxOutputTokens = 1024

type budgetScopeKey struct{}

// WithBudgetScope attaches scope to ctx; budgeted providers reserve
// against it in addition to the session ledger.
func WithBudgetScope(ctx context.Context, scope *BudgetScope) context.Context {
	return context.WithValue(ctx, budgetScopeKey{}, scope)
}

func budgetScopeFromContext(ctx context.Context) *BudgetScope {
	s, _ := ctx.Value(budgetScopeKey{}).(*BudgetScope)
	return s
}

// messageOverheadTokens bounds the per-message framing tokens a chat API
// adds around each message.
const messageOverheadTokens = 8

// inputTokenBound is an upper bound on a request's input tokens: one
// token never encodes less than one byte.
func inputTokenBound(req *llm.CompletionRequest) int {
	n := len(req.SystemPrompt) + messageOverheadTokens
	for _, m := range req.Messages {
		n += len(m.Content) + messageOverheadTokens
	}
	return n
}

// BudgetedProvider reserves every Complete call against a Ledger before
// sending it and reconciles the reservation to the reported token usage.
// It wraps the concrete provider, inside any rate limiter, so each retry
// reserves as its own call.
type BudgetedProvider struct {
	inner    llm.Provider
	ledger   *Ledger
	override *llm.Price // ATTEST_JUDGE_PRICE_IN/OUT; nil uses the price table
}

// NewBudgetedProvider wraps inner. override, when non-nil, prices every
// call regardless of model.
func NewBudgetedProvider(inner llm.Provider, ledger *Ledger, override *llm.Price) *BudgetedProvider {
	return &BudgetedProvider{inner: inner, ledger: ledger, override: override}
}

// Name delegates to the inner provider.
func (b *BudgetedProvider) Name() string { return b.inner.Name() }

// DefaultModel delegates to the inner provider.
func (b *BudgetedProvider) DefaultModel() string { return b.inner.DefaultModel() }

// PriceFor returns the price used for model.
func (b *BudgetedProvider) PriceFor(model string) (llm.Price, bool) {
	if b.override != nil {
		return *b.override, true
	}
	return llm.LookupPrice(model)
}

// Complete reserves, calls the inner provider, and settles.
func (b *BudgetedProvider) Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = b.inner.DefaultModel()
	}
	if req.MaxTokens <= 0 {
		capped := *req
		capped.MaxTokens = defaultMaxOutputTokens
		req = &capped
	}
	price, priced := b.PriceFor(model)
	maxOut := req.MaxTokens
	if ol, ok := b.inner.(llm.OutputLimiter); ok {
		maxOut = ol.MaxOutputTokens(req.MaxTokens)
	}
	var reserveUSD float64
	if priced {
		reserveUSD = price.Cost(inputTokenBound(req), maxOut)
	}
	scope := budgetScopeFromContext(ctx)
	if b.inner.Name() == "acp" && b.ledger.usdLimited(scope) {
		return nil, fmt.Errorf("%w: the acp judge reports no token usage, so a USD budget (max_cost_usd) cannot cover it; "+
			"limit acp calls with max_judge_calls", llm.ErrBudgetExhausted)
	}
	res, err := b.ledger.reserve(scope, true, reserveUSD, priced, model)
	if err != nil {
		return nil, err
	}

	resp, err := b.inner.Complete(ctx, req)
	if err == nil {
		resp = clampUsage(resp)
	}
	u := types.Usage{ModelCalls: 1, JudgeCalls: 1}
	if !priced {
		u.UnpricedCalls = 1
	}
	if err != nil {
		res.fail(u)
		return nil, err
	}
	if priced {
		// Copy rather than mutate: the provider may share its response.
		reconciled := *resp
		reconciled.Cost = price.Cost(resp.InputTokens, resp.OutputTokens)
		resp = &reconciled
	}
	u.InputTokens = int64(resp.InputTokens)
	u.OutputTokens = int64(resp.OutputTokens)
	u.CachedTokens = int64(min(resp.CachedInputTokens, resp.InputTokens))
	u.CostUSD = resp.Cost
	if llm.AttemptFromContext(ctx) > 0 {
		u.RetryCostUSD = resp.Cost
	}
	res.settle(u)
	return resp, nil
}

// UsageEmbedder is an embedder that reports the input tokens it consumed.
type UsageEmbedder interface {
	embedding.Embedder
	EmbedWithUsage(ctx context.Context, text string) ([]float32, int, error)
}

// BudgetedEmbedder reserves each paid embedding call against a Ledger.
// Embedding calls count toward the USD limit, not MaxJudgeCalls.
type BudgetedEmbedder struct {
	inner  UsageEmbedder
	ledger *Ledger
}

// NewBudgetedEmbedder wraps an embedder that reports usage.
func NewBudgetedEmbedder(inner UsageEmbedder, ledger *Ledger) *BudgetedEmbedder {
	return &BudgetedEmbedder{inner: inner, ledger: ledger}
}

// Model delegates to the inner embedder.
func (b *BudgetedEmbedder) Model() string { return b.inner.Model() }

// Embed reserves, embeds, and settles.
func (b *BudgetedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	model := b.inner.Model()
	price, priced := llm.LookupPrice(model)
	var reserveUSD float64
	if priced {
		reserveUSD = price.Cost(len(text)+messageOverheadTokens, 0)
	}
	res, err := b.ledger.reserve(budgetScopeFromContext(ctx), false, reserveUSD, priced, model)
	if err != nil {
		return nil, err
	}
	vec, tokens, err := b.inner.EmbedWithUsage(ctx, text)
	u := types.Usage{ModelCalls: 1}
	if !priced {
		u.UnpricedCalls = 1
	}
	if err != nil {
		res.fail(u)
		return nil, err
	}
	u.InputTokens = int64(max(tokens, 0))
	if priced {
		u.CostUSD = price.Cost(max(tokens, 0), 0)
	}
	res.settle(u)
	return vec, nil
}

// clampUsage returns resp with negative token counts reported by a
// provider raised to 0, so a malformed reply cannot credit the budget.
func clampUsage(resp *llm.CompletionResponse) *llm.CompletionResponse {
	if resp.InputTokens >= 0 && resp.OutputTokens >= 0 && resp.CachedInputTokens >= 0 {
		return resp
	}
	c := *resp
	c.InputTokens, c.OutputTokens, c.CachedInputTokens = max(c.InputTokens, 0), max(c.OutputTokens, 0), max(c.CachedInputTokens, 0)
	return &c
}
