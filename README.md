[![Attest](assets/banner.png)](https://attest-framework.github.io/)

---

# Attest

**Test your AI agents like you test your code.**

[Documentation](https://attest-framework.github.io/) | [Examples](https://github.com/attest-framework/attest-examples) | [Contributing](CONTRIBUTING.md)

[![CI](https://github.com/attest-framework/attest/actions/workflows/ci.yml/badge.svg)](https://github.com/attest-framework/attest/actions/workflows/ci.yml)
[![PyPI](https://img.shields.io/pypi/v/attest-ai)](https://pypi.org/project/attest-ai/)
[![npm](https://img.shields.io/npm/v/%40attest-ai%2Fcore)](https://www.npmjs.com/package/@attest-ai/core)
[![License](https://img.shields.io/github/license/attest-framework/attest)](LICENSE)

Attest is an open-source testing framework purpose-built for AI agents and LLM-powered systems. It treats deterministic assertions as first-class citizens alongside probabilistic evaluation. Deterministic checks run first; model-scored checks run last.

> **Status:** v1.0.1 — capture → replay → merge gate on real agent runs, six evaluated assertion layers plus plugins, Python and TypeScript adapters with a maturity tier each (see Key Features), behavior contracts, mutation testing, HTML failure viewer, MCP server + agent skills, judges on four provider families, continuous eval with drift detection, pytest + vitest integrations.

---

![Attest demo — the INC-1042 double refund becomes a replayed regression test: incident, capture, replay with zero model calls](assets/demo.gif)

## Why Attest

The current ecosystem defaults to LLM-as-judge for everything. This creates cost explosion, flaky tests, and slow suites. Attest provides six evaluated layers plus plugins, ordered from free and deterministic (layers 1–4) to model-scored (layers 5–6, whose cost depends on the configured provider), and reaches for the cheapest valid assertion first. The table below is generated from `attest-engine capabilities --json`.

<!-- claims:layers:start -->
| Layer | Name | Checks | Assertion types | Cost | Deterministic |
| --- | --- | --- | --- | --- | --- |
| 1 | Schema validation | Checks the output against a JSON Schema. | `schema` | free | yes |
| 2 | Constraint checks | Checks cost, token, latency and step-count limits. | `constraint` | free | yes |
| 3 | Trace and behavioral checks | Checks which tools ran, in which order, with which arguments, how often per business key, across the agent tree and against the world's final state. | `trace`, `trace_tree`, `effect` | free | yes |
| 4 | Content pattern matching | Checks the output text against substrings, patterns and keywords. | `content` | free | yes |
| 5 | Embedding similarity | Scores semantic similarity against a reference with an embedding model: OpenAI embeddings are paid, the local ONNX model is free. | `embedding` | depends on provider | no |
| 6 | LLM-as-judge | Scores the output against a rubric with a judge model: OpenAI, Anthropic and Gemini judges are paid, a local ACP agent or an OpenAI-compatible local endpoint is not. | `llm_judge` | depends on provider | no |
| plugins | Plugins | Custom assertions that run in the SDK process; cost and determinism are the plugin's. | `plugin` | plugin-defined | no |
<!-- claims:layers:end -->

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
- **Language SDKs** (Python, TypeScript) — thin, idiomatic wrappers that communicate with the engine via JSON-RPC 2.0 over stdio
- **Protocol** — SDK spawns engine as subprocess; each SDK release requires the engine version equal to its own (see [configuration](docs/guides/configuration.md))

```text
SDK (Python/TS) ──stdin/stdout──▶ Engine Process (Go)
                                        ├── Trace Processor
                                        ├── Assertion Pipeline (6 layers + plugins)
                                        ├── Result History (SQLite)
                                        ├── Drift Detection (σ-based)
                                        ├── Simulation Runtime
                                        └── Report Generator
```

## Key Features

Each feature carries a maturity tier; the table is generated from `attest-engine capabilities --json`.

<!-- claims:features:start -->
| Feature | Maturity |
| --- | --- |
| Capture, replay and merge gate | stable |
| Behavior and world-effect contracts | stable |
| CI policy, baselines and reports | stable |
| Failure viewer | beta |
| Mutation testing | beta |
| MCP server and agent skills | beta |
| LLM judges on four provider families | beta |
| Multi-agent trace trees | beta |
| Simulation runtime | beta |
| Plugin assertions | beta |
| Local ONNX embeddings | experimental |
| Continuous eval and drift detection | experimental |

- **stable**: Covered by the parity and conformance suites; a change in behavior is a breaking change.
- **beta**: Shipped and tested, but the interface or behavior can still change in a minor release.
- **experimental**: Shipped without a stability commitment and without demonstrated demand; it can change or be removed in any release.
<!-- claims:features:end -->

- **Six evaluated layers plus plugins** — layers 1–4 are free and deterministic; layers 5–6 are model-scored and cost what their provider costs
<!-- claims:adapters:start -->
- **10 Python adapters** — OpenAI (stable), Anthropic (stable), Manual (stable), Gemini (beta), Ollama (beta), LangChain (beta), CrewAI (beta), OpenTelemetry (beta), Google ADK (experimental), LlamaIndex (experimental)
- **7 TypeScript adapters** — OpenAI (stable), Anthropic (stable), Manual (stable), Gemini (beta), Ollama (beta), LangChain.js (beta), OpenTelemetry (beta)
<!-- claims:adapters:end -->
- **Soft failure budgets** — scores between 0.5–0.8 warn without blocking CI
- **Cost as a test metric** — assert on token usage, API cost, and latency
- **Soft-failure limit** — cap soft failures per engine session via `ATTEST_MAX_SOFT_FAILURES`
- **`attest-engine doctor`** — reports engine version, capabilities and judge/embedding configuration (`--json` for tooling); see [configuration](docs/guides/configuration.md)
- **CI reports and selection** — `attest report junit|summary|rows` render report-v2 as JUnit XML, a pull-request summary (pass / fail / not evaluated, replayed vs live model calls, usage and retry cost) and annotation rows; `attest select --base <ref>` lists the tests a diff needs. Guides: [policies](docs/guides/policies.md), [baselines](docs/guides/baselines.md), [selection](docs/guides/selection.md)
- **Mutation testing** — `attest mutate <fixtures> [--contracts DIR]` builds behavior mutants of your fixtures (drop, duplicate or reorder tool calls, perturb arguments, inject tool errors, alter world effects, truncate output), runs the suite's contracts against each at no model cost, and reports a mutation score, the surviving mutants and a contract that would kill each; the policy rule `min_mutation_score` gates on it. Guide: [mutation](docs/guides/mutation.md)
- **Failure viewer** — `attest view report.json [--baseline report.json] [--mutation mutate.json]` writes one self-contained offline HTML page (no scripts, no external resources, all trace content escaped): the test list, the trace tree with the failing span highlighted, expected versus actual, replay or live provenance, cost, a baseline-versus-head diff and surviving mutants; `ATTEST_REPORT_TRACES=1` embeds the traces in the report. Guide: [viewer](docs/guides/viewer.md)
- **Agent-facing CLI** — every command takes `--json` and prints one document that validates against a published schema (on an error it prints nothing to stdout: the exit code and stderr describe it); `attest help --json` lists commands with schemas and exit codes; `attest guide <topic>` prints the shipped guides offline; `attest init --skills` installs the attest agent skills (also published as Claude Code, Cursor and Codex plugins). Guides: [cli](docs/guides/cli.md), [agents](docs/guides/agents.md)
- **TypeScript parity** — every Python capability has a TypeScript counterpart or a documented exception, enforced by a generated matrix in the test suites; see [parity](docs/guides/parity.md)
- **MCP server** — `attest mcp` serves six fixed tools to coding agents over stdio (`trace_check`, `evaluate`, `explain_failure`, `draft_contract`, `run_contract`, `fixture_info`), read-only, offline and confined to the project root; `run_contract` parses a closed contract grammar, never code; `attest init --mcp` writes the Claude Code, Cursor and Codex configs. Guide: [mcp](docs/guides/mcp.md)
- **Judges on four provider families** — Layer 6 runs natively on OpenAI, Anthropic and Gemini, or any OpenAI-compatible endpoint via `ATTEST_JUDGE_BASE_URL`, each with its own rate limit, plus a local-only ACP judge on your Claude Code or Codex subscription; see [judges](docs/guides/judges.md)
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
- **Plugin system** — extend with custom assertions via `attest.plugins` entry points (Python) or an `"attest": {"plugins": [...]}` entry in an installed package's `package.json` (TypeScript, `loadPackagePlugins`); see [simulation](docs/guides/simulation.md#plugins-in-typescript)
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
│   └── typescript/     # TypeScript SDK (npm: @attest-ai/core)
├── .github/
│   └── actions/        # Reusable composite actions (setup-attest)
├── docs/               # Documentation
├── examples/           # Standalone example projects (incl. CI workflows)
└── scripts/            # Build and development scripts
```

## Quick Start

No API key, model or network access is needed: the sample test checks a recorded agent run with deterministic assertions.

### Python

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv init --bare my-agent-tests && cd my-agent-tests
uv add "attest-ai>=1.0.1" pytest
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

`--attest-report` writes every test with its outcome, file and line, and every assertion result, as a report-v2 JSON file (`proto/attest/v1/report-v2.schema.json`). `attest policy evaluate` checks it against `attest.policy.yaml`: exit 0 passes, 1 blocks (a hard failure), 2 warns (assertions not evaluated), 3 means the policy or report could not be loaded or the arguments were invalid.

<!-- snippet: setup=project -->
```bash
uv run pytest --attest-report=attest-report.json
uv run attest policy evaluate --report attest-report.json --policy attest.policy.yaml
uv run attest init --ci github
```

`attest init --ci github` writes `.github/workflows/attest.yml`: the test step records its outcome and continues, the report upload, policy evaluation and the `pr-annotate` action always run, and a last step fails the job when the tests failed. Failures are annotated at the failing test's file and line. See the [merge gate guide](docs/guides/merge-gate.md).

### TypeScript

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
npm init -y && npm pkg set type=module
npm install -D "@attest-ai/core@>=1.0.1" "@attest-ai/vitest@>=1.0.1" "vitest@^4"
npx attest init
npx vitest run
```

`npx attest init` writes `tests/my-agent.test.ts` and `attest.policy.yaml`; the line marked `ATTEST-DEMO` works the same way. `npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json` writes the report and `npx attest init --ci github` writes the workflow, which installs with `npm ci` from the `package-lock.json` that `npm install` writes.

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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, workflow, and code standards.

## License

[Apache License 2.0](LICENSE)
