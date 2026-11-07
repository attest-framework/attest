# Behavior contracts

A behavior contract states what the agent did, not what it said: how often a tool ran per business key, which tools it must never call, the shape and order of its tool calls, whether it looped, how many tool failures it hit in a row, and what arguments it passed. Contracts are Layer 3 trace checks. They run in the engine, deterministically, with no model calls, so they cost nothing and give the same answer on every run.

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai>=1.0.1"
npm install -D "@attest-ai/core@>=1.0.1" "@attest-ai/vitest@>=1.0.1" "vitest@^4"
```

## Example: one refund per order

The refund-desk incident INC-1042 refunded order `O-1042` twice: `issue_refund` timed out, the agent retried it, and both calls went through. The contract "`issue_refund` at most once per `order_id`" catches it on the frozen trace.

Python (pytest, with the `attest` fixture):

<!-- snippet: skip reason="needs the INC-1042 trace export from a real incident capture (the refund-desk demo repository)" -->
```python
from pathlib import Path

from attest import OTelAdapter, expect


def test_inc_1042_refunds_twice(attest):
    trace = OTelAdapter.from_otlp(Path("incidents/INC-1042/trace.py.otlp.json"))

    result = attest.inspect(
        expect(trace).tool_cardinality("issue_refund", per="order_id", at_most=1)
    )

    [row] = result.assertion_results
    assert row.status == "hard_fail"
    assert row.explanation == "issue_refund called 2× for order_id=O-1042 (max 1)"
```

TypeScript (Vitest, with the Attest plugin):

<!-- snippet: skip reason="needs the INC-1042 trace export from a real incident capture (the refund-desk demo repository)" -->
```typescript
import { readFileSync } from "node:fs";
import { it, expect } from "vitest";
import { OTelAdapter, attestExpect } from "@attest-ai/core";
import { inspect } from "@attest-ai/vitest";

it("INC-1042 refunds twice", async () => {
  const trace = await OTelAdapter.fromOtlp(
    readFileSync("incidents/INC-1042/trace.ts.otlp.json", "utf8"),
  );

  const result = await inspect(
    attestExpect(trace).toolCardinality("issue_refund", { per: "order_id", atMost: 1 }),
  );

  expect(result.assertionResults[0].status).toBe("hard_fail");
  expect(result.assertionResults[0].explanation).toBe(
    "issue_refund called 2× for order_id=O-1042 (max 1)",
  );
});
```

`inspect()` returns the results without failing the test, which is how a reproduction test asserts that a contract reports a known violation. Use `evaluate()` (Python `attest.evaluate(...)`, Vitest `evaluate(...)`) when the contract should fail the test.

## Contracts

| Python | TypeScript | Engine `check` | Holds when |
| --- | --- | --- | --- |
| `tool_cardinality(tool, *, per=None, at_most=None, at_least=None)` | `toolCardinality(tool, { per, atMost, atLeast })` | `tool_cardinality` | `tool` is called between `at_least` and `at_most` times, counted separately for each value of argument `per` when given. |
| `forbidden_tools(tools)` | `forbiddenTools(tools)` | `forbidden_tools` | None of `tools` is called. |
| `trajectory(tools, *, mode="strict")` | `trajectory(tools, { mode })` | `trajectory` | The tool calls match `tools` in the given mode (below). |
| `trajectory_partial_order(order, *, tools=None)` | `trajectoryPartialOrder(order, { tools })` | `trajectory`, mode `partial_order` | Every tool in `tools` and in `order` is called, and each `(before, after)` pair holds as in `tool_called_before`. Other calls may appear anywhere. |
| `tool_called_before(before, after)` | `toolCalledBefore(before, after)` | `tool_order` | Every call to `after` has an earlier call to `before`. Holds when `after` is never called. |
| `no_tool_cycles(period, *, min_repeats=2)` | `noToolCycles(period, { minRepeats })` | `cycle` | No block of `period` (1–4) tool calls repeats `min_repeats` or more times in a row. A→B→A→B fails `period=2`; A→A fails `period=1`. |
| `max_tool_failure_streak(max_streak)` | `maxToolFailureStreak(maxStreak)` | `failure_streak` | At most `max_streak` consecutive tool calls end in error. |
| `tool_arg_satisfies(tool, arg, op, value)` | `toolArgSatisfies(tool, arg, op, value)` | `arg_predicate` | Every call to `tool` has argument `arg` satisfying `op value`. Holds when `tool` is never called. |

Every method also takes `soft=True` (TypeScript `{ soft: true }`), which turns a failure into `soft_fail`.

Trajectory modes compare the list of tool calls with `tools`, counting repeats:

| Mode | Passes when |
| --- | --- |
| `strict` | The calls are exactly `tools`, in order. |
| `unordered` | The calls are `tools` in any order. |
| `subset` | Every call appears in `tools` (as often or less). |
| `superset` | Every entry of `tools` was called (at least as often). |

`tool_arg_satisfies` operators: `eq`, `ne`, `lt`, `lte`, `gt`, `gte` (numbers), `in`, `not_in` (a list of values). `arg` and `per` are dot paths into the call's arguments, such as `order_id` or `customer.tier`. Equality compares JSON values. A numeric operator on an argument that is not a number fails.

## World effect contracts

The contracts above read the trace. Effect contracts read what the agent did to the system it acts on: the records of a [world](world-fixtures.md). They are Layer 3 checks too, deterministic and free, and a contract statement names them like any other method.

| Statement | Python / TypeScript chain | Holds when |
| --- | --- | --- |
| `count("refunds", equals=1, order_id="O-1042")` | `.world(w).count(...)` | The matching records number between the bounds. |
| `unique("refunds", by=["order_id"])` | `.world(w).unique(...)` | No two matching records share the values of `by`. |
| `idempotent("refunds", key="idempotency_key")` | `.world(w).idempotent(...)` | Every matching record has a key, and no two share one. |
| `sum("refunds", "amount_cents", at_most=15000, status="paid")` | `.world(w).sum(...)` | The total of `amount_cents` over the matching records is at most the number. |
| `sum("refunds", "amount_cents", at_most=related("orders", "amount_cents", on="order_id"))` | `.world(w).sum(..., related(...))` | For each `order_id`, the refunds sum to at most that order's `amount_cents`. |
| `matches("notifications", "customer_id", related("orders", "customer_id", on="order_id"))` | `.world(w).matches(...)` | Each notification's `customer_id` equals its order's. |

`related(resource, field, on=key)` (TypeScript `related(resource, field, { on: key })`, imported from `attest` / `@attest-ai/core`) names the record of `resource` whose `key` field equals the checked record's, and the field to compare. A `sum` bound given as `related(...)` groups by `key`; a numeric bound compares the whole filtered total. The messages, the missing-record cases and the exponent limit are in [world fixtures](world-fixtures.md#relational-effects-totals-and-recipients).

The contract grammar stays closed. A statement still takes only literals; `related(...)` is the one call it accepts inside an argument, and only as the `at_most=` keyword of `sum` or the third argument of `matches`. Inside a list or a map, with a positional `on`, with a non-literal name, as the value of any other argument (`count(..., equals=related(...))`), or combined with an operator (`related(...) + 1`), it is rejected with a position, for example `calls are not allowed in arguments (position 48)`. [MCP](mcp.md#contract-statements) `run_contract` and `draft_contract` accept and draft these statements; the lines they return are the ones pasted into a test.

## What the contracts see

- **Tool calls only.** Contracts read `tool_call` steps. Model calls, retrievals and agent calls between them do not affect a trajectory, an ordering rule or a cycle. The six original trace checks (`tools_called_in_order`, `tools_called_exactly`, `no_tool_loops`, `no_duplicate_tools`, `required_tools`, `forbidden_tools`) still match steps of every type by name.
- **Retries count.** A step with `retry_of` is a separate call. A retried `issue_refund` is two refunds, which is what INC-1042 needs a contract to say.
- **Top-level steps.** Steps inside a nested `sub_trace` are not read; convert multi-agent exports with the OTLP adapters, which flatten them, or use the [multi-agent](multi-agent.md) trace-tree checks.
- **Tool failures need trace v2.** A failure is a step with an `error` (trace schema version 2, written by the OTLP adapters and `attest trace convert`). On a version 1 trace, `max_tool_failure_streak` returns `not_evaluated: missing_evidence` instead of guessing.

## Missing evidence

When a call lacks the argument a contract reads (`per` for `tool_cardinality`, `arg` for `tool_arg_satisfies`), the contract cannot judge that call. If another call already violates the contract, the result is a failure that names it; otherwise the result is `not_evaluated: missing_evidence`, naming the steps without the argument. A test with a `not_evaluated` result is skipped with that reason (fails under `--attest-strict`); see [verdicts.md](verdicts.md).

An invalid contract, such as `no_tool_cycles(5)` or a `tool_cardinality` without bounds, is a `hard_fail` whose explanation names the problem. `tool_cardinality` without `at_most` or `at_least` raises in the SDK before it reaches the engine.

## Engine specs

The DSL methods emit `type: "trace"` assertions; both SDKs emit identical specs, checked by `protocol-tests/contracts/dsl.json` in the engine, Python and TypeScript suites. The spec fields are listed in `proto/attest/v1/protocol-spec.md` (Layer 3). For example:

```json
{"check": "tool_cardinality", "tool": "issue_refund", "per": "order_id", "max": 1, "soft": false}
```
