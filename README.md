# Attest

**Test your AI agents like you test your code.**

Attest is an open-source testing framework purpose-built for AI agents and LLM-powered systems. It treats deterministic assertions as first-class citizens alongside probabilistic evaluation — because 70% of your agent's testable surface is deterministic.

> **Status:** v0.3.0 — Layers 1–6 + simulation runtime + multi-agent trace trees. Engine, Python SDK, ONNX embeddings, LLM judge, simulated users, fault injection, and cross-agent assertions shipped.

---

## Why Attest

The current ecosystem defaults to LLM-as-judge for everything. This creates cost explosion, flaky tests, and slow suites. Attest provides a graduated 7-layer assertion model that reaches for the cheapest valid assertion first:

```
Layer 1: Schema Validation        — free, instant, deterministic
Layer 2: Constraint Checks        — free, instant, deterministic
Layer 3: Trace/Behavioral Checks  — free, instant, deterministic
Layer 4: Content Pattern Matching  — free, instant, near-deterministic
Layer 5: Embedding Similarity      — ~$0.001, ~100ms, near-deterministic
Layer 6: LLM-as-Judge             — ~$0.01+, ~1-3s, non-deterministic
Layer 7: Trace Tree (Multi-Agent)  — free, instant, deterministic
```

## What It Looks Like

```python
import attest
from attest import expect

class TestRefundAgent(attest.AgentTest):
    async def test_processes_eligible_refund(self):
        result = await self.agent.run("Refund order ORD-123456")

        # Layer 1 — schema validation (free, instant)
        expect(result).tool_calls_to_match_schema()

        # Layer 2 — constraint checks (free, instant)
        expect(result).to_cost_less_than(0.05)

        # Layer 3 — trace inspection (free, instant)
        expect(result).to_call_tools_in_order(["lookup_order", "process_refund"])

        # Layer 4 — content matching (free, instant)
        expect(result).output_to_contain("refund")
        expect(result).output_not_to_contain("sorry")

        # Layer 6 — LLM-as-judge (only when needed)
        expect(result).to_pass_judge("Is the response helpful and accurate?")
```

## Architecture

Attest follows a **protocol-first, engine-centric architecture** inspired by LSP and Playwright:

- **Core engine** (Go) — single binary, handles all evaluation, assertion, and simulation logic
- **Language SDKs** (Python, TypeScript, Go) — thin, idiomatic wrappers that communicate with the engine via JSON-RPC 2.0 over stdio
- **Protocol** — SDK spawns engine as subprocess; capability negotiation decouples SDK and engine release cycles

```text
SDK (Python/TS/Go) ──stdin/stdout──▶ Engine Process (Go)
                                        ├── Trace Processor
                                        ├── 6-Layer Assertion Pipeline
                                        ├── Simulation Runtime
                                        └── Report Generator
```

## Key Features

- **7-layer assertion pipeline** — graduated from free/deterministic to paid/probabilistic
- **Soft failure budgets** — scores between 0.5–0.8 warn without blocking CI
- **Cost as a test metric** — assert on token usage, API cost, and latency
- **Framework-agnostic** — adapters for OpenAI, Anthropic, Gemini, Ollama, OTel
- **Local ONNX embeddings** — optional all-MiniLM-L6-v2 provider, zero API cost for Layer 5
- **Judge meta-evaluation** — 3x judge runs with median scoring and variance detection
- **CI-ready** — composite GitHub Action, tiered testing workflow, adversarial hardening
- **Simulation runtime** — simulated users with personas, mock tools, fault injection, multi-turn orchestration
- **Multi-agent testing** — hierarchical trace trees, cross-agent assertions, delegation tracking, aggregate metrics
- **Single binary engine** — no runtime dependencies, cross-platform

## Repository Layout

```test
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

### Prerequisites

- Go 1.24+
- Python 3.10+
- [uv](https://docs.astral.sh/uv/getting-started/installation/)

### Build and Test

```bash
git clone https://github.com/attest-ai/attest.git
cd attest

# Build engine + install Python SDK
make dev-setup

# Run all tests
make test

# Verify engine
./bin/attest-engine version
# attest-engine 0.3.0
```

## Roadmap

| Phase | Version | Status       | Description                                                          |
| ----- | ------- | ------------ | -------------------------------------------------------------------- |
| 0     | —       | **Complete** | Repository scaffolding, toolchain, protocol spec                     |
| 1     | v0.1    | **Complete** | Go engine (Layers 1–4), Python SDK, pytest plugin, 4 LLM adapters    |
| 2     | v0.2    | **Complete** | Layers 5–6 (embeddings, LLM-as-judge), soft failures, CI integration |
| 3     | v0.3    | **In Progress** | Simulation runtime, multi-agent testing (**done**); TypeScript SDK (pending) |
| 4     | v0.4    | Planned      | Continuous eval, plugin system, LangChain/LlamaIndex adapters        |
| 5     | v0.5    | Planned      | Go SDK, Attest Cloud MVP, benchmark registry                         |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, workflow, and code standards.

## License

[Apache License 2.0](LICENSE)
