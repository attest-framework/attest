# Write a contract for an agent behavior

Goal: a test that encodes a rule such as "never charge a card twice", runs the agent's real code with zero model calls, and passes on the current agent. Background: `attest guide behavior-contracts`, `attest guide world-fixtures`, `attest guide replay`.

## 1. Find the agent, its tools and its evidence

```bash
git ls-files '*.attest.json' '*world.yaml' '*.otlp.json' 'incidents/*'
git grep -n -E 'def (run|handle|invoke)\b|export (async )?function (run|handle|invoke)\b|tools\.json|"type": "function"|execute_tool|tool_call'
git grep -l -E 'attest_replay|useReplay|attest_world|useWorld|attest\.evaluate|evaluate\('
```

Write down the repo's own facts, before any code. The final column is one fictional checkout assistant example, not names or paths to assume in the target repo:

| Item | Where to look | Checkout assistant example |
| --- | --- | --- |
| Entry point the tests can call | agent modules, CLI modules, existing tests | `shop_assistant.agent.handle(config, message, tracer)`; TS `handle(loadConfig(), message, tracer)` |
| How it builds its model client | configuration loaders and client constructors | reads `OPENAI_BASE_URL` / `OPENAI_API_KEY` when `load_config()` (TS `loadConfig()`) runs |
| Tool names and the business key in their arguments | tool schemas (`tools.json`, function definitions) | `charge_card(order_id, amount_cents)`; key `order_id` |
| Where side effects go | HTTP clients and their base URL variables | billing API at `BILLING_BASE_URL` |
| Fixture with recorded model calls | `tests/attest/fixtures/<name>.attest.json` | `checkout-timeout` |
| World for the external system | `*.world.yaml` (`base_url_env`, `faults`) | `world/billing-timeout.world.yaml` with `timeout_after_commit`, collection `charges` |
| Incident input and frozen trace | `incidents/<ID>/` | `incidents/INC-7/request.json`, `incidents/INC-7/trace.otlp.json` |
| How tests already run, with which env | `Makefile`, `package.json` scripts, CI workflow | `make test` |

No fixture yet: follow `capture-incident.md` first, then return here. No trace at all (the agent emits no OpenTelemetry spans and uses no adapter): read `attest guide framework-adapters`.

## 2. Choose the contract

| The rule is about | Use | Needs |
| --- | --- | --- |
| which tools the agent calls, how often per key, in what order, with which arguments | trace contract: `tool_cardinality`, `forbidden_tools`, `tool_called_before`, `trajectory`, `tool_arg_satisfies`, `no_tool_cycles`, `max_tool_failure_streak` | a trace (replayed run, fixture trace or OTLP export) |
| what ended up in the external system (records created, duplicates, idempotency keys, totals or related recipients) | world-effect contract: `world(state).count`, `.unique`, `.idempotent`, `.sum`, `.matches`, with `related()` for cross-resource comparisons | a `world.yaml` for the system, and the agent reading its base URL from the world's `base_url_env` variable |

Decision rule: a duplicate the model causes (it calls the tool twice) shows in the trace. A duplicate below the tool (an HTTP retry after a timeout that already committed) leaves one tool call and two records: only the effect contract catches it. For "prevent duplicate X", write both when a world exists; write the trace contract alone when none exists, and tell the user the effect is unchecked.

Do not write a world from scratch unless the user asks; a world covers HTTP/JSON create/list/get only (`attest guide world-fixtures`).

## 3. Write the test

Put it next to the repo's existing replay tests (same directory, same naming). Construct model and HTTP clients after `attest_replay` / `useReplay` and `attest_world` / `useWorld` have set the environment.

Example: the fictional checkout assistant discovered above. Python (pytest; the `attest`, `attest_replay` and `attest_world` fixtures come from the installed plugin):

```python
import json
from pathlib import Path

from attest import OTelAdapter, expect
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

from shop_assistant.agent import handle, load_config

ROOT = Path(__file__).resolve().parents[3]
MESSAGE = json.loads((ROOT / "incidents/INC-7/request.json").read_text())["customer_message"]


def test_checkout_charges_order_once(attest, attest_replay, attest_world):
    world = attest_world(ROOT / "world/billing-timeout.world.yaml")
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    with attest_replay("checkout-timeout"):
        handle(load_config(), MESSAGE, provider.get_tracer("verify"))
    trace = OTelAdapter.from_spans(exporter.get_finished_spans())
    state = world.state()

    attest.evaluate(
        expect(trace)
        .tool_cardinality("charge_card", per="order_id", at_most=1)
        .world(state)
        .count("charges", equals=1, order_id="A-17")
        .world(state)
        .idempotent("charges", order_id="A-17")
    )
```

TypeScript (Vitest; no plugin configuration is required):

```ts
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { OTelAdapter, attestExpect } from "@attest-ai/core";
import { evaluate, useReplay, useWorld } from "@attest-ai/vitest";
import { BasicTracerProvider, InMemorySpanExporter, SimpleSpanProcessor } from "@opentelemetry/sdk-trace-base";
import { it } from "vitest";
import { handle, loadConfig } from "../../src/agent.js";

const root = new URL("../../../", import.meta.url);
const { customer_message } = JSON.parse(
  readFileSync(new URL("incidents/INC-7/request.json", root), "utf8"),
) as { customer_message: string };

it("INC-7: charges the order once", async (context) => {
  const world = await useWorld(fileURLToPath(new URL("world/billing-timeout.world.yaml", root)));
  const exporter = new InMemorySpanExporter();
  const provider = new BasicTracerProvider({ spanProcessors: [new SimpleSpanProcessor(exporter)] });
  await useReplay("checkout-timeout", { mode: "strict" });
  await handle(loadConfig(), customer_message, provider.getTracer("verify"));
  const trace = await OTelAdapter.fromSpans(exporter.getFinishedSpans());
  const state = await world.state();

  await evaluate(
    attestExpect(trace)
      .toolCardinality("charge_card", { per: "order_id", atMost: 1 })
      .world(state)
      .count("charges", { equals: 1, where: { order_id: "A-17" } })
      .world(state)
      .idempotent("charges", { where: { order_id: "A-17" } }),
    { context },
  );
});
```

Adapt names, paths and the entry point to the repo. Rules for the chain:

- Each effect check (`count`, `unique`, `idempotent`, `sum`, `matches`) returns the chain, not the world; call `.world(state)` again before the next one.
- `evaluate` enforces (fails or skips the test). `inspect` only returns results: use it for a reproduction that asserts the frozen incident trace still `hard_fail`s, never for the guarding contract.
- Every chain must be evaluated; in 1.1.0, building it without evaluating fails the test. Evaluate again after adding assertions.
- `attest_replay(name)` reads `tests/attest/fixtures/<name>.attest.json` under the pytest root; `useReplay(name)` reads it under the working directory. Pass `fixtures_dir` / `fixturesDir` when the repo keeps fixtures elsewhere.
- A frozen trace without the agent: `OTelAdapter.from_otlp(Path("incidents/INC-7/trace.otlp.json"))`, `load_fixture(path).trace` (Python) or `loadFixture(path)` from `@attest-ai/core` (TS).

## 4. Run it with zero model calls

Use the repo's own target when one exists (`make test`, `npm test`). Otherwise give the agent's settings placeholder values, never real keys:

```bash
# Python
OPENAI_API_KEY=replay-needs-no-key <other settings the agent requires> \
  uv run pytest tests/replay/test_no_duplicate_charges.py \
  --attest-replay=strict --attest-egress=block --attest-strict

# TypeScript
OPENAI_API_KEY=replay-needs-no-key ATTEST_REPLAY=strict ATTEST_EGRESS=block ATTEST_STRICT=1 \
  npx vitest run tests/replay/no-duplicate-charges.test.ts
```

Done looks like:

```text
attest replay: 4 replayed · 0 live model calls · $0.00
1 passed
```

(Vitest prints `Tests  1 passed (1)`; the replay counts are in the report.)

`0 live model calls` is required. `1 skipped` is not done: read the reason (step 5).

## 5. Read the result and fix

| Result | Cause | Fix |
| --- | --- | --- |
| `replay miss: <component> changed …` | the agent's prompt, tools, params or model differ from the recording, or the input differs from the captured run | use the captured input exactly; if the agent change is intended, re-record (`capture-incident.md`, `--refresh`) |
| `attest egress guard: connection to … blocked` | a model client was built before the replay started, or with a hard-coded base URL | construct the client after `attest_replay` / `useReplay` |
| `not_evaluated: missing_evidence` on `tool_cardinality` | tool spans lack the `per` argument (`gen_ai.tool.call.arguments`) | record arguments on tool spans; check with `attest trace check <export> --json` |
| `not_evaluated: missing_evidence` on an effect check | `world(None)`: the test ran without a world | start `attest_world` / `useWorld` before the agent |
| `effect violated: 2 charges for A-17 (expected 1)` | the duplicate is real | this is the bug; fix the agent (idempotency key, check before retry), not the contract |
| `charge_card called 2× for order_id=A-17 (max 1)` on the current agent | the model retries the tool | fix the agent; if the retry is intended and idempotent, keep only the effect contract and tell the user |
| a `RuntimeError` / missing env from the agent's settings | the agent needs variables the test did not set | set placeholders as the repo's Makefile or CI does |
| the world's `base_url_env` (example: `BILLING_BASE_URL`) ignored | the HTTP client was built before `attest_world` | construct it after the call |

More diagnosis: `debug-failure.md`. Finish by reporting the test file, the command, the result line and any `not_evaluated` reason.
