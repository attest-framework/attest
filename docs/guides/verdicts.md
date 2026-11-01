# Verdicts: pass, fail, skip

Attest reports one of four statuses for every assertion you request, and turns them into a test outcome only after the whole batch has run.

## Assertion statuses

| Status | Meaning |
| --- | --- |
| `pass` | The assertion held. |
| `soft_fail` | The assertion missed a `soft` threshold. |
| `hard_fail` | The assertion failed. |
| `not_evaluated` | The engine produced no verdict. `reason` says why. |

| Reason | When |
| --- | --- |
| `provider_unavailable` | The embedding or judge provider is configured but the call failed (outage, timeout, unusable response). |
| `provider_not_configured` | No embedding or judge provider is configured, for example no judge API key. |
| `simulation` | `ATTEST_SIMULATION=1`: layers 1–4 run for real, layers 5–6 are not called. |
| `missing_evidence` | The trace does not record the checked field, for example `metadata.cost_usd` unset. |
| `short_circuited` | A layer 5–6 assertion did not run because a layer 1–4 assertion in the same batch hard-failed. |

A `not_evaluated` assertion is never a pass and never a `broken_code` failure.

## Test outcome

| Batch contains | pytest outcome |
| --- | --- |
| any `hard_fail` | **failed**, with a diagnostic for every failed and not-evaluated assertion |
| otherwise any `not_evaluated` | **skipped** with `not evaluated: <reasons>` (failed with `--attest-strict`) |
| otherwise any `soft_fail` | passed; the session summary lists the soft failures |
| otherwise | passed |

```text
test_refund.py::test_loop FAILED
test_refund.py::test_judge_only SKIPPED (not evaluated: provider_not_configured)
```

`result.failed_assertions` holds `hard_fail` and `soft_fail` rows only; `result.not_evaluated` holds the rest. `result.pass_rate` divides passes by evaluated assertions only and is `None` when nothing was evaluated.

## `evaluate()` and `inspect()`

```python
def test_refund(attest):
    result = refund_agent(order_id="ORD-123")
    attest.evaluate(expect(result).no_tool_loops("lookup_order").passes_judge("Helpful?"))
```

`attest.evaluate(chain)` enforces the outcome: it fails or skips the running test. `attest.inspect(chain)` returns the same `AgentResult` without enforcing, for trial loops and custom checks:

```python
from attest.simulation import repeat

@repeat(n=5)
def trials(attest):
    return attest.inspect(expect(refund_agent(order_id="ORD-1")).output_contains("refund"))

def test_refund_reliability(attest):
    run = trials(attest)
    assert run.outcome() != "fail"          # aggregate verdict over all trials
    assert run.pass_rate is None or run.pass_rate >= 0.8
```

## Policy

`attest-engine policy evaluate` adds a `not_evaluated` violation when any assertion is `not_evaluated`. Its severity is `warn` (exit 2) by default:

```yaml
# attest.policy.yaml
not_evaluated: block   # exit 1 instead of 2
```

## Simulation mode

`ATTEST_SIMULATION=1` (or `attest.config(simulation=True)` before the engine starts) starts the engine with providers disabled. Deterministic layers still evaluate, so a violated schema, constraint, trace or content contract still fails; judge and embedding assertions report `not_evaluated: simulation`.
