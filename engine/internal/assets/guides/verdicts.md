# Verdicts: pass, fail, skip

Attest reports one of four statuses for every assertion you request, and turns them into a test outcome only after the whole batch has run.

## Assertion statuses

| Status | Meaning |
| --- | --- |
| `pass` | The assertion held. |
| `soft_fail` | The assertion missed a `soft` threshold. |
| `hard_fail` | The assertion failed. A `threshold="dynamic"` score below its history bar is a `hard_fail` too ([dynamic thresholds](judges.md#dynamic-thresholds)). |
| `not_evaluated` | The engine produced no verdict. `reason` says why. |

| Reason | When |
| --- | --- |
| `provider_unavailable` | The embedding or judge provider is configured but the call failed (outage, timeout, rate limit or server error that outlasted the retries). |
| `provider_not_configured` | No embedding or judge provider is configured, for example no judge API key. |
| `simulation` | `ATTEST_SIMULATION=1`: layers 1–4 run for real, layers 5–6 are not called. |
| `missing_evidence` | The trace does not record the checked field, for example `metadata.cost_usd` unset. |
| `short_circuited` | A layer 5–6 assertion did not run because a layer 1–4 assertion in the same batch hard-failed. |
| `budget_exhausted` | An embedding or judge call could not reserve its cost or call count against the session budget (`ATTEST_MAX_JUDGE_CALLS`, `ATTEST_BUDGET_USD`) or the test's budget, so it was not sent. |
| `judge_abstained` | The judge answered `{"abstain": true, "reason": ...}`: the output lacks the evidence the rubric needs. The reason is in the explanation. |
| `judge_malformed` | The judge answered, but not with a `score` between 0 and 1 or an explicit abstain: no JSON object, no `score`, a non-numeric or out-of-range score such as `7`, or a reply that contains the output delimiter. The judge measured nothing, so no score is recorded. |
| `provider_auth` | The provider rejected the credentials (HTTP 401 or 403). Reported after one call, without retries. |

A `not_evaluated` assertion is never a pass and never a `broken_code` failure. A `hard_fail` or `soft_fail` row can carry a `failure_class`; `spec_error` marks an assertion whose spec cannot be evaluated (unknown rubric, `repeat` outside 1–16, unknown bias probe, missing `target`, `threshold` outside (0, 1]): the test is wrong, not the agent. It is a failure class only, never a `reason`.

## Test outcome

| Batch contains | pytest / Vitest outcome |
| --- | --- |
| any `hard_fail` | **failed**, with a diagnostic for every failed and not-evaluated assertion |
| otherwise any `not_evaluated` | **skipped** with `not evaluated: <reasons>` (failed with `--attest-strict` / `ATTEST_STRICT=1`) |
| otherwise any `soft_fail` | passed; the session summary lists the soft failures |
| otherwise | passed |

```text
test_refund.py::test_loop FAILED
test_refund.py::test_judge_only SKIPPED (not evaluated: provider_not_configured)
```

`result.failed_assertions` holds `hard_fail` and `soft_fail` rows only; `result.not_evaluated` holds the rest. `result.pass_rate` divides passes by evaluated assertions only and is `None` when nothing was evaluated.

## `evaluate()` and `inspect()`

<!-- snippet: fragment reason="excerpt: relies on `refund_agent`, defined outside the snippet" -->
```python
def test_refund(attest):
    result = refund_agent(order_id="ORD-123")
    attest.evaluate(expect(result).no_tool_loops("lookup_order").passes_judge("Helpful?"))
```

`attest.evaluate(chain)` enforces the outcome: it fails or skips the running test. `attest.inspect(chain)` returns the same `AgentResult` without enforcing, for trial loops and custom checks:

<!-- snippet: fragment reason="excerpt: relies on `expect`, defined outside the snippet" -->
```python
from attest.simulation import repeat

@repeat(n=5)
def trials(attest):
    return attest.inspect(expect(refund_agent(order_id="ORD-1")).output_contains("refund"))

def test_refund_reliability(attest):
    run = attest.enforce(trials(attest))    # fail / skip on the aggregate verdict
    assert run.pass_rate >= 0.8             # safe: enforce() skipped if nothing was evaluated
```

`attest.enforce(result)` applies the same rules (and `--attest-strict`) to an `AgentResult` from `inspect()`, a `RepeatResult` or a `ScenarioResult`: any failing trial fails the test with that trial's diagnostics; otherwise any not-evaluated trial skips it with the summed reasons, for example `not evaluated: simulation ×5`. A trial with soft failures counts as passing; passed and skipped tests with soft failures are listed in the "Attest soft failures" session summary.

### Vitest

<!-- snippet: fragment reason="excerpt: relies on `it`, defined outside the snippet" -->
```ts
import { evaluate, inspect } from "@attest-ai/vitest";

it("refund", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-123" })).noToolLoops("lookup_order"));
});
```

`evaluate(chain)` throws with the diagnostics on any hard failure and skips the running test (`↓ refund [not evaluated: provider_not_configured]`) when an assertion was not evaluated; pass `{ strict: true }` or set `ATTEST_STRICT=1` to fail instead. Under `it.concurrent`, or on Vitest before 4.1 (Vitest 3 included), pass the test context so the right test is skipped; without it on those versions `evaluate()` fails with a "pass the test context" error instead of skipping: `it.concurrent("refund", async (context) => evaluate(chain, { context }))`. `inspect(chain)` never enforces. `AgentResult` exposes `outcome()`, `notEvaluated`, `notEvaluatedReasons`, `passRate` and `skipReason()`. Each Vitest worker starts its own engine on first use when the client of `globalSetup: ["@attest-ai/vitest/setup"]` is not visible to it; that engine exits with the worker.

## Policy

`attest-engine policy evaluate` adds a `not_evaluated` violation when any assertion is `not_evaluated`. Its severity is `warn` (exit 2) by default:

```yaml
# attest.policy.yaml
not_evaluated: block   # exit 1 instead of 2
```

With `--baseline <tag>`, a `regression` rule compares each assertion's pass rate with the pinned baseline using a Wilson interval and prints `verdict` (`pass`, `regressed` or `inconclusive`) with per-assertion `trials` in the JSON output. `regressed` blocks (exit 1, or `severity: warn`), `inconclusive` warns (exit 2):

```yaml
regression:
  confidence: 0.95   # two-sided Wilson interval (default)
  tolerance: 0.05    # allowed drop below the baseline pass rate (default)
```

An assertion regresses when the interval's upper bound is below the baseline pass rate minus the tolerance, passes when its lower bound is at or above that line, and is inconclusive when the interval straddles it. Pass rates exclude `not_evaluated` runs. The rule applies only to assertions with at least 2 runs in both the report and the baseline; single runs keep the status comparison of `block_on_regression`. An assertion run as trials that cannot be compared (baseline pinned from one run, or never evaluated on one side) is reported `inconclusive` and stays under `block_on_regression`. `attest baseline pin` stores each assertion's passes and evaluated runs, folding the rows of fixed-N trials into one entry; baselines pinned by earlier versions count as one run each.

## Simulation mode

`ATTEST_SIMULATION=1` (or `attest.config(simulation=True)` / `config({ simulation: true })` before the engine starts) starts the engine with providers disabled. Deterministic layers still evaluate, so a violated schema, constraint, trace or content contract still fails; judge and embedding assertions report `not_evaluated: simulation`.
