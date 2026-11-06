# Statistical verdicts and cost budgets

A non-deterministic agent can pass a test once and fail it the next time, so one run cannot show that a change made it worse. Attest runs each test a fixed number of times, compares every assertion's pass rate with a pinned baseline using a confidence interval, and returns `pass`, `regressed` or `inconclusive`. Every model call Attest makes for the evaluation reserves its cost against a budget before it is sent, so trials and judges cannot overspend.

## 1. Run fixed-N trials

| Runner | How |
|---|---|
| pytest | `uv run pytest --attest-trials=20 --attest-report=attest-report.json` runs every test that uses the `attest` fixture 20 times |
| Vitest | wrap the test body in `trials()` and run `ATTEST_TRIALS=20 npx vitest run` with the `AttestReporter` (`--reporter=@attest-ai/vitest/reporter`) |

```python
def test_refund_reply(attest):
    result = refund_desk("Order O-2001 arrived torn. Please refund it.")
    attest.evaluate(expect(result).output_contains("refund of $26.50").passes_judge(CONFIRMS_REFUND))
```

```typescript
import { trials, evaluate } from "@attest-ai/vitest";

it("refund reply", async (context) => {
  await trials(async () => {
    const result = await refundDesk("Order O-2001 arrived torn. Please refund it.");
    await evaluate(attestExpect(result).outputContains("refund of $26.50"));
  }, { context });
});
```

During a trial, `evaluate()` records the result without failing or skipping the test, as `inspect()` does; only the aggregate of the trials decides the outcome:

| Trials | Test outcome |
|---|---|
| no trial evaluated an assertion | skipped with the not-evaluated reasons, or `no assertions in N trials` (failed with `--attest-strict`) |
| pass rate of the evaluated trials below the minimum pass rate | failed: `<passed>/<evaluated> trials passed (pass rate <r>) below min pass rate <min>`, then the diagnostics of the failing trials |
| otherwise | passed; the summary prints `<test>: <passed>/<evaluated> trials passed (<n> not evaluated)` |

The minimum pass rate defaults to 1.0: any failing trial fails the test. Lower it for an agent that is allowed to miss sometimes:

| Scope | pytest | Vitest |
|---|---|---|
| Run | `--attest-min-pass-rate=0.8` | `ATTEST_MIN_PASS_RATE=0.8`, or `attestGlobalSetup({ minPassRate: 0.8 })` from `@attest-ai/vitest/setup` in a global-setup file (below) |
| One test (overrides the run) | `@pytest.mark.attest_min_pass_rate(0.8)` | `trials(body, { minPassRate: 0.8 })` |

Vitest takes global setup as a file path, so the factory lives in a file of your own. Import it from the `/setup` subpath, never the package root, so the Vitest 3 config loads:

```ts
// attest-setup.ts
import { attestGlobalSetup } from "@attest-ai/vitest/setup";

export const { setup, teardown } = attestGlobalSetup({ minPassRate: 0.8 });
```

Point `globalSetup: ["./attest-setup.ts"]` in the Vitest config at it. Without options, `globalSetup: ["@attest-ai/vitest/setup"]` does the same with a minimum pass rate of 1.0.

Trials whose assertions were not evaluated are left out of the pass rate. The minimum pass rate decides this run's test outcome; whether the pass rate dropped against the reference version is the policy's question (section 3).

A trial that raises (for example when the agent's model call fails) is a failed trial: its rows are replaced by one `hard_fail` row per assertion of the test, with the exception in the explanation (and the engine's status when the trial had already evaluated that assertion), so every trial contributes exactly one row per assertion. A pytest `skip` or `fail` inside a trial ends the test as usual.

Within a test, assertion ids are derived from the test id and the assertion's position, so the rows of one assertion share an id across trials and across runs. Each report row carries its 1-based `trial`, and the test row carries `trials`. Trials run one after another; pytest supports synchronous tests only (use `attest.simulation.repeat` with `inspect_async()` in `async` tests).

## 2. Pin a baseline

Run the trials on the reference version (for example `main`) and pin the report:

```bash
uv run pytest --attest-trials=20 --attest-report=baseline.json
uv run attest baseline pin --tag main --report baseline.json
```

`attest baseline show --tag main` lists each assertion's `passes` and `evaluated` runs. Runs whose assertion was `not_evaluated` are counted separately and never enter a pass rate.

## 3. Compare with the policy

```yaml
# attest.policy.yaml
regression:
  confidence: 0.95   # two-sided Wilson interval (default)
  tolerance: 0.05    # allowed drop below the baseline pass rate (default)
```

```bash
uv run attest policy evaluate --policy attest.policy.yaml --report attest-report.json --baseline main
```

The rule compares assertions that have at least 2 runs in both the report and the baseline. Single runs keep the status comparison of `block_on_regression`; an assertion run as trials that cannot be compared (a baseline pinned from a single run, or no evaluated run on one side) is reported `inconclusive` and stays under `block_on_regression`. For each compared assertion the engine computes the Wilson interval of the current pass rate and draws a line at the baseline pass rate minus the tolerance:

| Interval | Verdict | Exit |
|---|---|---|
| upper bound below the line | `regressed` | 1 (`severity: warn` makes it 2) |
| lower bound at or above the line | `pass` | 0 |
| straddles the line | `inconclusive` | 2 |

The JSON output has the overall `verdict` and a `trials` list with each assertion's passes, evaluated runs, interval, baseline rate and line. An assertion absent from the baseline is not compared. When the report has only single runs, the rule gives no verdict and the status comparison decides; when the report has trials and none could be compared, the verdict is `inconclusive`. Other policy rules (`not_evaluated`, `layers`, `max_cost_usd`) still apply to the same report.

With a baseline of 18/20 (0.90) the line is 0.85:

| Current | 95% Wilson interval | Verdict |
|---|---|---|
| 16/20 | [0.584, 0.919] | inconclusive |
| 8/20 | [0.219, 0.613] | regressed |
| 20/20 | [0.839, 1.000] | inconclusive |
| 73/73 | [0.950, 1.000] | pass |

`inconclusive` means the trials cannot tell the difference at this confidence: run more trials rather than rerunning until the verdict changes. The number of trials is fixed before the run; stopping early when a verdict looks settled (sequential testing) is not supported, because repeated checks of a Wilson interval overstate confidence.

## 4. Budgets

Every model call Attest makes for an evaluation — layer 5 OpenAI embeddings, layer 6 judge calls, `repeat` runs, bias probes, simulated-user messages and retries — reserves one call and its worst-case cost before it is sent, and is reconciled to the token usage the provider reports when it returns. A call that cannot reserve is not sent, and its assertion is `not_evaluated: budget_exhausted`; the test is skipped and the policy warns (exit 2) unless it sets `not_evaluated: block`.

| Scope | Calls | USD |
|---|---|---|
| Engine session | `ATTEST_MAX_JUDGE_CALLS` | `ATTEST_BUDGET_USD` |
| One test (pytest) | `@pytest.mark.attest_budget(max_judge_calls=10)` | `@pytest.mark.attest_budget(max_cost_usd=0.50)` |
| One test (Vitest) | `testBudget: { maxJudgeCalls: 10 }` on `evaluate` or `inspect`, `budget: { maxJudgeCalls: 10 }` on `trials` | `maxCostUsd: 0.5` in the same objects |

A call must fit both the session and the test budget. Calls count judge-provider requests; USD covers judge and embedding calls. Prices come from a built-in table (`gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`, `text-embedding-3-small`, `text-embedding-3-large`) or `ATTEST_JUDGE_PRICE_IN` / `ATTEST_JUDGE_PRICE_OUT` in USD per 1M tokens. A USD session budget with a judge or embedding model that has no price is a startup error, and so is a USD budget with the `acp` judge, which reports no token usage; limit it with `ATTEST_MAX_JUDGE_CALLS`.

### In-flight exposure

A reservation holds the most a call can cost: the input price × the prompt size in bytes (plus 8 per message), which no tokenizer exceeds, and the output price × the output-token limit actually sent (256 for judge calls; Anthropic and Gemini raise it to 4096). Calls in flight never hold more than the remaining budget together, and reconciled spend never exceeds the reservation, so the attest-initiated spend stays within the budget. Because the reservation is pessimistic, the last calls of a nearly spent budget can be refused although their actual cost would have fit.

The agent's own model calls do not go through Attest and are never reserved. Attest reads the agent's spend from each trace's `metadata.cost_usd` and checks a test's `max_cost_usd` only between trials: when the attest spend plus the agent spend reaches it, no further trial starts and the summary prints `stopped after <k> of <N> trials: test budget spent`. A trial that is running finishes, so the agent can exceed a test budget by up to one trial's spend; the session budget (`ATTEST_BUDGET_USD`) covers attest calls only.

### Usage summary

The run summary prints one line, and the report records it in `summary.usage`:

```text
attest usage: 42 model calls · 51234 tokens (12000 cached) · $0.1123 · retry cost $0.0040 · 3 calls refused by budget
```

Model calls include failed requests; cached tokens are provider prompt-cache reads; retry cost is the part of the spend on retried requests. Calls to a model without a price are counted as `unpriced calls not in $`.

## Related

- [Verdicts](verdicts.md): statuses, reasons and the policy's `not_evaluated` rule.
- [Judges](judges.md): providers, abstention and rate limits.
- [Configuration](configuration.md#evaluation): every budget variable.
