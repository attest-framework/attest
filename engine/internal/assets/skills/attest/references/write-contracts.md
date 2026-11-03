# Write a contract for an agent behavior

Goal: a test that encodes a rule such as "never refund an order twice", runs the agent's real code with zero model calls, and passes on the current agent. Background: `attest guide behavior-contracts`, `attest guide world-fixtures`, `attest guide replay`.

## 1. Find the agent, its tools and its evidence

```bash
git ls-files '*.attest.json' '*world.yaml' '*.otlp.json' 'incidents/*'
git grep -n -E 'def run\b|export (async )?function run\b|tools\.json|"type": "function"|execute_tool|tool_call'
git grep -l -E 'attest_replay|useReplay|attest_world|useWorld|attest\.evaluate|evaluate\('
```

Write down, before any code:

| Item | Where to look | Refund example |
| --- | --- | --- |
| Entry point the tests can call | `src/**/agent.*`, CLI modules, existing tests | `refund_desk.agent.run(settings, message, tracer)`; TS `run(settingsFromEnv(), message, tracer)` |
| How it builds its model client | settings read from env | reads `OPENAI_BASE_URL` / `OPENAI_API_KEY` when `Settings.from_env()` runs |
| Tool names and the business key in their arguments | tool schemas (`tools.json`, function definitions) | `issue_refund(order_id, amount_cents, reason)`; key `order_id` |
| Where side effects go | HTTP clients and their base URL variables | payments API at `PAYMENTS_BASE_URL` |
| Fixture with recorded model calls | `tests/attest/fixtures/<name>.attest.json` | `inc-1042` |
| World for the external system | `*.world.yaml` (`base_url_env`, `faults`) | `world/payments-inc-1042.world.yaml` with `timeout_after_commit` |
| Incident input and frozen trace | `incidents/<ID>/` | `request.json`, `trace.py.otlp.json` |
| How tests already run, with which env | `Makefile`, `package.json` scripts, CI workflow | `make check-replay` |

No fixture yet: follow `capture-incident.md` first, then return here. No trace at all (the agent emits no OpenTelemetry spans and uses no adapter): read `attest guide framework-adapters`.

## 2. Choose the contract

| The rule is about | Use | Needs |
| --- | --- | --- |
| which tools the agent calls, how often per key, in what order, with which arguments | trace contract: `tool_cardinality`, `forbidden_tools`, `tool_called_before`, `trajectory`, `tool_arg_satisfies`, `no_tool_cycles`, `max_tool_failure_streak` | a trace (replayed run, fixture trace or OTLP export) |
| what ended up in the external system (records created, duplicates, idempotency keys) | world-effect contract: `world(state).count`, `.unique`, `.idempotent` | a `world.yaml` for the system, and the agent reading its base URL from the world's `base_url_env` variable |

Decision rule: a duplicate the model causes (it calls the tool twice) shows in the trace. A duplicate below the tool (an HTTP retry after a timeout that already committed) leaves one tool call and two records: only the effect contract catches it. For "prevent duplicate X", write both when a world exists; write the trace contract alone when none exists, and tell the user the effect is unchecked.

Do not write a world from scratch unless the user asks; a world covers HTTP/JSON create/list/get only (`attest guide world-fixtures`).

## 3. Write the test

Put it next to the repo's existing replay tests (same directory, same naming). Construct model and HTTP clients after `attest_replay` / `useReplay` and `attest_world` / `useWorld` have set the environment.

Python (pytest; the `attest`, `attest_replay` and `attest_world` fixtures come from the installed plugin):

```python
import json
from pathlib import Path

from attest import OTelAdapter, expect
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

from refund_desk.agent import Settings, run

ROOT = Path(__file__).resolve().parents[3]
MESSAGE = json.loads((ROOT / "incidents/INC-1042/request.json").read_text())["customer_message"]


def test_inc_1042_refunds_order_once(attest, attest_replay, attest_world):
    world = attest_world(ROOT / "world/payments-inc-1042.world.yaml")
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    with attest_replay("inc-1042"):
        run(Settings.from_env(), MESSAGE, provider.get_tracer("verify"))
    trace = OTelAdapter.from_spans(exporter.get_finished_spans())
    state = world.state()

    attest.evaluate(
        expect(trace)
        .tool_cardinality("issue_refund", per="order_id", at_most=1)
        .world(state)
        .count("refunds", equals=1, order_id="O-1042")
        .world(state)
        .idempotent("refunds", order_id="O-1042")
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
import { run, settingsFromEnv } from "../../src/agent.js";

const root = new URL("../../../", import.meta.url);
const { customer_message } = JSON.parse(
  readFileSync(new URL("incidents/INC-1042/request.json", root), "utf8"),
) as { customer_message: string };

it("INC-1042: refunds the order once", async () => {
  const world = await useWorld(fileURLToPath(new URL("world/payments-inc-1042.world.yaml", root)));
  const exporter = new InMemorySpanExporter();
  const provider = new BasicTracerProvider({ spanProcessors: [new SimpleSpanProcessor(exporter)] });
  await useReplay("inc-1042", { mode: "strict" });
  await run(settingsFromEnv(), customer_message, provider.getTracer("verify"));
  const trace = await OTelAdapter.fromSpans(exporter.getFinishedSpans());
  const state = await world.state();

  await evaluate(
    attestExpect(trace)
      .toolCardinality("issue_refund", { per: "order_id", atMost: 1 })
      .world(state)
      .count("refunds", { equals: 1, where: { order_id: "O-1042" } })
      .world(state)
      .idempotent("refunds", { where: { order_id: "O-1042" } }),
  );
});
```

Adapt names, paths and the entry point to the repo. Rules for the chain:

- Each effect check (`count`, `unique`, `idempotent`) returns the chain, not the world; call `.world(state)` again before the next one.
- `evaluate` enforces (fails or skips the test). `inspect` only returns results: use it for a reproduction that asserts the frozen incident trace still `hard_fail`s, never for the guarding contract.
- `attest_replay(name)` reads `tests/attest/fixtures/<name>.attest.json` under the pytest root; `useReplay(name)` reads it under the working directory. Pass `fixtures_dir` / `fixturesDir` when the repo keeps fixtures elsewhere.
- A frozen trace without the agent: `OTelAdapter.from_otlp(Path("incidents/X/trace.otlp.json"))`, `load_fixture(path).trace` (Python) or `loadFixture(path)` from `@attest-ai/core` (TS).

## 4. Run it with zero model calls

Use the repo's own target when one exists (`make check-replay`, `npm test`). Otherwise give the agent's settings placeholder values, never real keys:

```bash
# Python
OPENAI_API_KEY=replay-needs-no-key <other settings the agent requires> \
  uv run pytest tests/replay/test_no_duplicate_refunds.py \
  --attest-replay=strict --attest-egress=block --attest-strict

# TypeScript
OPENAI_API_KEY=replay-needs-no-key ATTEST_REPLAY=strict ATTEST_EGRESS=block ATTEST_STRICT=1 \
  npx vitest run tests/replay/no-duplicate-refunds.test.ts
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
| `effect violated: 2 refunds for O-1042 (expected 1)` | the duplicate is real | this is the bug; fix the agent (idempotency key, check before retry), not the contract |
| `issue_refund called 2× for order_id=O-1042 (max 1)` on the current agent | the model retries the tool | fix the agent; if the retry is intended and idempotent, keep only the effect contract and tell the user |
| a `RuntimeError` / missing env from the agent's settings | the agent needs variables the test did not set | set placeholders as the repo's Makefile or CI does |
| `PAYMENTS_BASE_URL` (or similar) ignored | the HTTP client was built before `attest_world` | construct it after the call |

More diagnosis: `debug-failure.md`. Finish by reporting the test file, the command, the result line and any `not_evaluated` reason.
