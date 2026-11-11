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
| `simulation` | `ATTEST_SIMULATION=1`: deterministic checks run for real, layers 5–6 are not called. |
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

### Every chain must be evaluated

An otherwise-passing pytest test fails if it builds an `expect()` chain without passing it to `evaluate()` or `inspect()`; skipped or already-failing pytest tests keep their outcome. With the Vitest integration loaded, unevaluated chains also fail their owning test. The error is `1 expect() chain built but never evaluated` (or `N expect() chains built but never evaluated` for multiple chains). An empty chain does not count as a check: evaluating it also fails. Chains built outside a running test, such as at module import, are not tracked.

Evaluation covers the assertions present when it starts, not future additions. Appending an assertion after `evaluate()` or `inspect()` reopens the chain; evaluate it again before the test finishes. The second evaluation runs the whole chain, not just the appended assertion. Within the same test/trial scope, existing assertions retain their `assertion_id` on re-evaluation; new identical assertions get distinct occurrence indices.

```python
from attest import expect
from attest.result import AgentResult
from attest.trace import TraceBuilder

def test_extend_chain(attest):
    result = AgentResult(trace=TraceBuilder("agent").set_output(message="refund issued").build())
    chain = expect(result).output_contains("refund")
    attest.evaluate(chain)
    first_id = chain.assertions[0].assertion_id
    chain.output_contains("issued")  # The chain is no longer fully evaluated.
    attest.evaluate(chain)          # Evaluate both assertions.
    assert chain.assertions[0].assertion_id == first_id
```

In pytest, tracking starts before fixture setup, even for tests that do not request the `attest` fixture. A function-scoped fixture's chain belongs to the test whose setup builds it; a module- or session-scoped fixture's chain belongs to the first test item that executes that fixture's setup. Evaluate it during setup or in that test. Receiving an unchanged cached chain in a later test does not transfer that responsibility. Appending assertions to a shared chain registers it with the **mutating test**, which must evaluate it again; an unevaluated mutation is not charged to subsequent tests that only receive the cached value.

### Vitest

<!-- snippet: fragment reason="excerpt: relies on `it`, defined outside the snippet" -->
```ts
import { evaluate, inspect } from "@attest-ai/vitest";

it("refund", async () => {
  await evaluate(attestExpect(refundAgent.run({ order_id: "ORD-123" })).noToolLoops("lookup_order"));
});
```

`evaluate(chain)` throws with the diagnostics on any hard failure and skips the running test (`↓ refund [not evaluated: provider_not_configured]`) when an assertion was not evaluated; pass `{ strict: true }` or set `ATTEST_STRICT=1` to fail instead. Under `it.concurrent`, or on Vitest before 4.1 (Vitest 3 included), pass the test context so the right test is skipped; without it on those versions `evaluate()` fails with a "pass the test context" error instead of skipping: `it.concurrent("refund", async (context) => evaluate(chain, { context }))`. `inspect(chain)` never enforces. `AgentResult` exposes `outcome()`, `notEvaluated`, `notEvaluatedReasons`, `passRate` and `skipReason()`. Each Vitest worker starts its own engine on first use when the client of `globalSetup: ["@attest-ai/vitest/setup"]` is not visible to it; that engine exits with the worker.

The chain guard also covers `it.concurrent`: an unevaluated chain fails its owning test, not a concurrently running sibling. Pass `{ context }` to `evaluate()`, `inspect()` and `trials()` in concurrent tests so evaluations and report rows are attributed to the right test:

```ts
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "@attest-ai/vitest";

it.concurrent("checks the refund", async (context) => {
  const trace = new TraceBuilder("agent").setOutput({ message: "refund issued" }).build();
  const chain = attestExpect(trace).outputContains("refund");
  await evaluate(chain, { context });
  chain.outputContains("issued");
  await evaluate(chain, { context });
});
```

Vitest 4.1 and later use `aroundEach` to bind the guard across hooks and the test body. On Vitest before 4.1, the integration binds the test body, but cannot safely bind chains built in concurrent hooks or finish handlers: those fail fast with `Attest cannot bind this concurrent test's chain guard` and guidance to build the chain inside the test body or upgrade to Vitest >=4.1.

Keep test isolation enabled. With `isolate: false`, a later test file can reuse the cached integration without reinstalling its hooks; concurrent chains outside a bound scope fail with the same guidance, including `Enable test isolation if the integration is cached`. Do not rely on `isolate: false` for per-file hook coverage.

## Policy

`attest-engine policy evaluate` adds a `not_evaluated` violation when any assertion is `not_evaluated`. Its severity is `warn` (exit 2) by default:

```yaml
# attest.policy.yaml
not_evaluated: block   # exit 1 instead of 2
```

In 1.1.0, `tests_failed` also blocks by default: a failed pytest or Vitest test blocks the policy even if all of its Attest assertions passed. Use `tests_failed: warn` or `tests_failed: off` only when you intentionally want a non-blocking result. Missing or null `tests`, or a test outcome other than `passed`, `failed` or `skipped`, is invalid report input (exit 3), not a passing gate.

With `--baseline <tag>`, a `regression` rule compares each assertion's pass rate with the pinned baseline using a Wilson interval and prints `verdict` (`pass`, `regressed` or `inconclusive`) with per-assertion `trials` in the JSON output. `regressed` blocks (exit 1, or `severity: warn`), `inconclusive` warns (exit 2):

```yaml
regression:
  confidence: 0.95   # two-sided Wilson interval (default)
  tolerance: 0.05    # allowed drop below the baseline pass rate (default)
```

An assertion regresses when the interval's upper bound is below the baseline pass rate minus the tolerance, passes when its lower bound is at or above that line, and is inconclusive when the interval straddles it. Pass rates exclude `not_evaluated` runs. The rule applies only to assertions with at least 2 runs in both the report and the baseline; single runs keep the status comparison of `block_on_regression`. An assertion run as trials that cannot be compared (baseline pinned from one run, or never evaluated on one side) is reported `inconclusive` and stays under `block_on_regression`. `attest baseline pin` stores each assertion's passes and evaluated runs, folding the rows of fixed-N trials into one entry; baselines pinned by earlier versions count as one run each.

## Simulation mode

`ATTEST_SIMULATION=1` (or `attest.config(simulation=True)` / `config({ simulation: true })` before the engine starts) starts the engine with providers disabled. Deterministic layers still evaluate, so a violated schema, constraint, trace or content contract still fails; judge and embedding assertions report `not_evaluated: simulation`.
