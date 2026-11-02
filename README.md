[![Attest](assets/banner.png)](https://attest-framework.github.io/attest/)

---

# Attest

**Test your AI agents like you test your code.**

[Documentation](https://attest-framework.github.io/attest-website/) | [Examples](https://github.com/attest-framework/attest-examples) | [Contributing](CONTRIBUTING.md)

Attest is an open-source testing framework purpose-built for AI agents and LLM-powered systems. It treats deterministic assertions as first-class citizens alongside probabilistic evaluation — because 70% of your agent's testable surface is deterministic.

> **Status:** v0.6.0 — 8 assertion layers, 11 adapters (Python) + 7 adapters (TypeScript incl. LangChain.js), continuous eval with drift detection, dynamic thresholds, plugin system, result history (SQLite), CLI (Python + TypeScript), budget tracking, engine timeouts, concurrent evaluation, schema caching, branded types, CJS/ESM dual output, pytest + vitest integrations (incl. simulation mode), `queryDrift` + `generateUserMessage` SDK methods, 4 built-in simulation personas, comprehensive test coverage.

---

## Why Attest

The current ecosystem defaults to LLM-as-judge for everything. This creates cost explosion, flaky tests, and slow suites. Attest provides a graduated 8-layer assertion model that reaches for the cheapest valid assertion first:

```text
Layer 1: Schema Validation        — free, instant, deterministic
Layer 2: Constraint Checks        — free, instant, deterministic
Layer 3: Trace/Behavioral Checks  — free, instant, deterministic
Layer 4: Content Pattern Matching  — free, instant, near-deterministic
Layer 5: Embedding Similarity      — ~$0.001, ~100ms, near-deterministic
Layer 6: LLM-as-Judge             — ~$0.01+, ~1-3s, non-deterministic
Layer 7: Simulation               — free (mocked), variable
Layer 8: Multi-Agent              — free, instant, deterministic
```

## What It Looks Like

```python
from attest import agent, expect
from attest.plugin import AttestEngineFixture


@agent("refund-agent")
def refund_agent(builder, order_id):
    builder.add_tool_call(
        "lookup_order",
        args={"order_id": order_id},
        result={"status": "delivered", "amount": 89.99},
    )
    builder.add_tool_call(
        "process_refund", args={"order_id": order_id}, result={"refund_id": "RFD-001"}
    )
    builder.set_metadata(cost_usd=0.005, total_tokens=150, latency_ms=1200)
    return {
        "message": "Your refund of $89.99 has been processed.",
        "structured": {"refund_id": "RFD-001"},
    }


def test_refund(attest: AttestEngineFixture) -> None:
    result = refund_agent(order_id="ORD-123456")

    chain = (
        expect(result)
        # Layer 1 — schema (free, instant)
        .output_matches_schema({"type": "object", "required": ["refund_id"]})
        # Layer 2 — constraints (free, instant)
        .cost_under(0.05)
        # Layer 3 — trace (free, instant)
        .tools_called_in_order(["lookup_order", "process_refund"])
        # Layer 4 — content (free, instant)
        .output_contains("refund")
        .output_not_contains("sorry")
    )

    attest.evaluate(chain)
```

`attest.evaluate(chain)` sends the whole chain to the engine and fails the test on any hard failure, with the layer, expected and actual value of each failed assertion. Add `.passes_judge("Is the response helpful?")` (layer 6) when a deterministic check cannot express the requirement; without a configured judge it is reported as not evaluated and the test is skipped with the reason, never passed.

## Architecture

Attest follows a **protocol-first, engine-centric architecture** inspired by LSP and Playwright:

- **Core engine** (Go) — single binary, handles all evaluation, assertion, and simulation logic
- **Language SDKs** (Python, TypeScript, Go) — thin, idiomatic wrappers that communicate with the engine via JSON-RPC 2.0 over stdio
- **Protocol** — SDK spawns engine as subprocess; capability negotiation decouples SDK and engine release cycles

```text
SDK (Python/TS/Go) ──stdin/stdout──▶ Engine Process (Go)
                                        ├── Trace Processor
                                        ├── 8-Layer Assertion Pipeline
                                        ├── Result History (SQLite)
                                        ├── Drift Detection (σ-based)
                                        ├── Simulation Runtime
                                        └── Report Generator
```

## Key Features

- **8-layer assertion pipeline** — graduated from free/deterministic to paid/probabilistic
- **11 Python adapters** — OpenAI, Anthropic, Gemini, Ollama, LangChain, Google ADK, LlamaIndex, CrewAI, OTel, Manual, Simulation
- **7 TypeScript adapters** — OpenAI, Anthropic, Gemini, Ollama, LangChain.js, OTel, Manual
- **Soft failure budgets** — scores between 0.5–0.8 warn without blocking CI
- **Cost as a test metric** — assert on token usage, API cost, and latency
- **Soft-failure limit** — cap soft failures per engine session via `ATTEST_MAX_SOFT_FAILURES`
- **`attest-engine doctor`** — reports engine version, capabilities and judge/embedding configuration (`--json` for tooling); see [configuration](docs/guides/configuration.md)
- **Judges on four provider families** — Layer 6 runs natively on OpenAI, Anthropic and Gemini, or any OpenAI-compatible endpoint via `ATTEST_JUDGE_BASE_URL`, each with its own rate limit; see [judges](docs/guides/judges.md)
- **Python & TypeScript SDKs** — `attest-ai` (PyPI) + `@attest-ai/core` / `@attest-ai/vitest` (npm)
- **`attest init`** — scaffolds a passing sample test, a merge policy and, with `--ci github`, a GitHub Actions merge gate (Python + TypeScript)
- **CJS/ESM dual output** — TypeScript SDK supports both `require()` and `import`, tree-shakeable
- **Branded types** — `TraceId`, `AssertionId`, `AgentId` prevent accidental string assignment (TypeScript)
- **Discriminated union specs** — type-safe assertion specs per layer (TypeScript)
- **Local ONNX embeddings** — optional all-MiniLM-L6-v2 provider, zero API cost for Layer 5
- **Judge meta-evaluation** — 3x judge runs with median scoring and variance detection
- **CI-ready** — composite GitHub Action, tiered testing workflow, adversarial hardening
- **Simulation runtime** — simulated users with personas, mock tools, fault injection, multi-turn orchestration
- **Multi-agent testing** — hierarchical trace trees, cross-agent assertions, delegation tracking, temporal assertions, aggregate metrics
- **Continuous eval & drift detection** — sample production traces, σ-based drift alerts to webhooks/Slack (Python + TypeScript)
- **Plugin system** — extend with custom assertions via `attest.plugins` entry points (Python + TypeScript)
- **Result history** — SQLite-backed with configurable retention, automatic pruning
- **Engine performance** — schema compiler cache, deferred LRU writes, batch SQL eviction, `segmentio/encoding` (2.4x JSON speedup)
- **Configurable timeouts** — engine read timeout, concurrent request support, judge cache sizing
- **CLI** — `attest init` scaffolding; `attest doctor`, `baseline`, `policy` and every other command forwarded to a version-checked engine (Python + TypeScript)
- **Single binary engine** — no runtime dependencies, cross-platform

## Repository Layout

```text
attest/
├── proto/              # Protocol specification (JSON-RPC 2.0)
├── engine/             # Core engine (Go)
│   ├── cmd/            # CLI entrypoint
│   ├── internal/       # Assertion pipeline, trace model, simulation
│   └── pkg/            # Public Go packages
├── sdks/
│   ├── python/         # Python SDK (PyPI: attest-ai)
│   ├── typescript/     # TypeScript SDK (npm: @attest-ai/core)
│   └── go/             # Go SDK
├── .github/
│   └── actions/        # Reusable composite actions (setup-attest)
├── docs/               # Documentation
├── examples/           # Standalone example projects (incl. CI workflows)
└── scripts/            # Build and development scripts
```

## Quick Start

No API key, model or network access is needed: the sample test checks a recorded agent run with deterministic assertions.

### Python

```bash
uv init --bare my-agent-tests && cd my-agent-tests
uv add attest-ai pytest
uv run attest init
uv run pytest
```

`pytest` prints `1 passed`. `attest init` wrote `tests/test_my_agent.py` and `attest.policy.yaml`. The test checks a refund agent's run, built by hand with `TraceBuilder` in the `my_agent_result` fixture:

```python
"""Sample Attest test written by `attest init`. Run it with `pytest`.

`my_agent_result` is the trace of one agent run, built by hand with TraceBuilder, so
the sample needs no model, API key or network. Replace it with a trace from your
agent: wrap the agent with @attest.agent or record it with an adapter such as
attest.OpenAIAdapter.
"""

from __future__ import annotations

import pytest

from attest import Trace, TraceBuilder, expect
from attest.plugin import AttestEngineFixture


@pytest.fixture
def my_agent_result() -> Trace:
    return (
        TraceBuilder(agent_id="refund-agent")
        .set_input(user_message="Please refund order ORD-1001.")
        .add_tool_call(
            "lookup_order",
            args={"order_id": "ORD-1001"},
            result={"status": "delivered", "amount": 42.0},
        )
        .add_tool_call(
            "issue_refund",
            args={"order_id": "ORD-1001", "amount": 42.0},
            result={"refund_id": "RF-1001"},
        )
        .set_output(
            message="Your refund of $42.00 has been issued.",
            structured={"refund_id": "RF-1001", "amount": 42.0},
        )
        .set_metadata(total_tokens=180, cost_usd=0.002, latency_ms=900)
        .build()
    )


def test_my_agent(attest: AttestEngineFixture, my_agent_result: Trace) -> None:
    chain = (
        expect(my_agent_result)
        .output_matches_schema({"type": "object", "required": ["refund_id"]})  # layer 1: schema
        .cost_under(0.01)  # layer 2: constraint
        .tools_called_in_order(["lookup_order", "issue_refund"])  # layer 3: trace
        .output_contains("refund")  # ATTEST-DEMO: change "refund" to "store credit"
    )
    attest.evaluate(chain)
```

Change `"refund"` to `"store credit"` on the line marked `ATTEST-DEMO` and run `uv run pytest` again: `1 failed`, exit code 1, with the failed assertion's layer, expected and actual value:

```text
Attest diagnostic — 1 of 4 assertions failed:
  FAIL assert_2d1d647b — L4 content
      trace path: output.message
      expected:   contains "store credit"
      actual:     Your refund of $42.00 has been issued.
```

### Gate merges on it

`--attest-report` writes every test with its outcome, file and line, and every assertion result, as a report-v2 JSON file (`proto/attest/v1/report-v2.schema.json`). `attest policy evaluate` checks it against `attest.policy.yaml`: exit 0 passes, 1 blocks (a hard failure), 2 warns (assertions not evaluated).

```bash
uv run pytest --attest-report=attest-report.json
uv run attest policy evaluate --report attest-report.json --policy attest.policy.yaml
uv run attest init --ci github
```

`attest init --ci github` writes `.github/workflows/attest.yml`: the test step records its outcome and continues, the report upload, policy evaluation and the `pr-annotate` action always run, and a last step fails the job when the tests failed. Failures are annotated at the failing test's file and line. See the [merge gate guide](docs/guides/merge-gate.md).

### TypeScript

```bash
npm init -y && npm pkg set type=module
npm install -D @attest-ai/core @attest-ai/vitest vitest
npx attest init
npx vitest run
```

`npx attest init` writes `tests/my-agent.test.ts` and `attest.policy.yaml`; the line marked `ATTEST-DEMO` works the same way. `npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json` writes the report and `npx attest init --ci github` writes the workflow.

### Build from source

See [CONTRIBUTING.md](CONTRIBUTING.md) to build the engine and run the SDK test suites.

## Roadmap

| Phase | Version | Status       | Description                                                                                                                      |
| ----- | ------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| 0     | —       | **Complete** | Repository scaffolding, toolchain, protocol spec                                                                                 |
| 1     | v0.1    | **Complete** | Go engine (Layers 1–4), Python SDK, pytest plugin, 4 LLM adapters                                                                |
| 2     | v0.2    | **Complete** | Layers 5–6 (embeddings, LLM-as-judge), soft failures, CI integration                                                             |
| 3     | v0.3    | **Complete** | Simulation runtime, multi-agent testing, TypeScript SDK, framework adapters, temporal assertions                                 |
| 4     | v0.4    | **Complete** | Continuous eval, drift detection, plugin system, CrewAI adapter, result history, CLI                                             |
| 5     | v0.5.0  | **Complete** | The Alignment Release — engine/SDK version unification, schema cache, segmentio/encoding, full TypeScript parity, test coverage  |
| 6     | v0.6.0  | **Complete** | SDK parity with engine — `queryDrift`, `generateUserMessage`, dynamic thresholds, vitest simulation mode, 4 personas, sampleRate |
| 7     | v0.7    | Planned      | Go SDK, Attest Cloud MVP, benchmark registry                                                                                     |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, workflow, and code standards.

## License

[Apache License 2.0](LICENSE)
