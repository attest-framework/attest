# Attest: Test Your AI Agents Like You Test Your Code

AI agents are going to production without real testing.

57% of organizations now run agents in production. Yet most evaluation relies on LLM-as-judge — a probabilistic system evaluating a probabilistic system. The result: eval suites that cost 10x more than the agent itself, produce non-deterministic results, and still miss the failures that matter most — wrong tool calls, budget overruns, broken output schemas, violated state machines.

The industry treats agent evaluation as an AI problem. It's a testing problem.

Today we're open-sourcing **Attest**, a testing framework for AI agents that applies a simple discipline: reach for the cheapest valid assertion first, and only escalate when necessary.

## The Core Idea: Graduated Assertions

60–70% of an agent's testable surface is fully deterministic. Tool call schemas, execution order, cost budgets, content presence/absence, structured output format, state transitions. Routing all of this through an LLM judge is a category error — expensive, slow, and unnecessarily non-deterministic.

Attest implements an 8-layer assertion pipeline that exhausts deterministic checks before touching probabilistic ones:

| Layer                  | What It Tests                                                | Cost          | Speed    |
| ---------------------- | ------------------------------------------------------------ | ------------- | -------- |
| 1. Schema Validation   | Output structure, required fields, types                     | Free          | <1ms     |
| 2. Cost & Performance  | Token budgets, latency limits, cost caps                     | Free          | <1ms     |
| 3. Trace Structure     | Tool ordering, required/forbidden tools, loop detection      | Free          | <1ms     |
| 4. Content Validation  | Contains, not-contains, regex patterns                       | Free          | <5ms     |
| 5. Semantic Similarity | Meaning-level comparison via local ONNX embeddings           | Free (local)  | ~100ms   |
| 6. LLM-as-Judge        | Rubric-based scoring for subjective quality                  | ~$0.01        | ~1–3s    |
| 7. Simulation          | Persona-driven users, mock tools, fault injection            | Free (mocked) | Variable |
| 8. Multi-Agent         | Delegation chains, cross-agent assertions, aggregate metrics | Free          | ~5ms     |

Layers 1–4 cover the deterministic 70% at zero cost. Layer 5 runs semantic similarity locally with ONNX — no API key, no network call. Layer 6, the expensive LLM judge, is reserved for the genuinely subjective remainder. Layers 7–8 add simulation and multi-agent testing that no other open-source tool provides.

## What It Looks Like

```python
from attest import agent, expect
from attest.trace import TraceBuilder

@agent("support-agent")
def support_agent(builder: TraceBuilder, user_message: str):
    builder.add_llm_call(name="gpt-4.1", args={"model": "gpt-4.1"}, result={...})
    builder.add_tool_call(name="lookup_user", args={"query": user_message}, result={...})
    builder.add_tool_call(name="reset_password", args={"user_id": "U-123"}, result={...})
    builder.set_metadata(total_tokens=150, cost_usd=0.005, latency_ms=1200)
    return {"message": "Your temporary password is abc123.", "structured": {...}}

def test_support_agent(attest):
    result = support_agent(user_message="Reset my password")

    chain = (
        expect(result)
        .output_matches_schema({"type": "object", "required": ["message"]})  # L1
        .cost_under(0.05)                                                    # L2
        .tools_called_in_order(["lookup_user", "reset_password"])             # L3
        .output_contains("temporary password")                               # L4
        .output_similar_to("password has been reset", threshold=0.8)         # L5
        .passes_judge("Was the password reset handled correctly?")           # L6
    )
    attest.evaluate(chain)
```

No dashboard to configure. No YAML to author. Tests live next to code, run in CI, fail the build when agents break. See [`examples/quickstart/`](https://github.com/attest-framework/attest/tree/main/examples/quickstart) for the full working version.

TypeScript works the same way:

```typescript
import { Agent, TraceBuilder, attestExpect } from "@attest-ai/core";

const supportAgent = new Agent("support-agent", (builder: TraceBuilder, args) => {
  builder.addToolCall("lookup_user", { args: { query: args.user_message }, result: {...} });
  builder.addToolCall("reset_password", { args: { user_id: "U-123" }, result: {...} });
  builder.setMetadata({ total_tokens: 150, cost_usd: 0.005, latency_ms: 1200 });
  return { message: "Your temporary password is abc123." };
});

const result = supportAgent.run({ user_message: "Reset my password" });

attestExpect(result)
  .outputContains("temporary password")
  .costUnder(0.05)
  .toolsCalledInOrder(["lookup_user", "reset_password"])
  .passesJudge("Was the password reset handled correctly?");
```

See [`sdks/typescript/examples/customer-service/`](https://github.com/attest-framework/attest/tree/main/sdks/typescript/examples/customer-service) for the full TypeScript example covering all 8 layers.

## Architecture

```text
SDK (Python / TypeScript) ──stdio──▶ Engine (Go binary)
                                        ├── 8-Layer Assertion Pipeline
                                        ├── Result History (SQLite)
                                        ├── Drift Detection (σ-based)
                                        ├── Simulation Runtime
                                        └── Plugin System
```

The engine is a single Go binary with zero runtime dependencies. Six platform targets (macOS, Linux, Windows × amd64/arm64). Cold start: 1.7–3.2ms. 100-step trace evaluation: <2ms.

SDKs are thin idiomatic wrappers — all evaluation logic lives in the engine. This means the Python SDK, TypeScript SDK, and any future SDK produce identical assertion results. No reimplementation divergence.

## 11 Adapters, Zero Lock-In

Attest works with whatever you're building on:

**Provider adapters** — OpenAI, Anthropic, Gemini, Ollama
**Framework adapters** — LangChain, Google ADK, LlamaIndex, CrewAI, OpenTelemetry
**Manual adapter** — For custom agent architectures
**Simulation adapter** — For testing without real API calls

Each adapter translates framework-specific events into Attest's universal trace format. Write your assertions once; swap providers without changing tests.

## Continuous Evaluation & Drift Detection

Testing in CI is table stakes. Attest also supports continuous evaluation in production:

```python
from attest import ContinuousEvalRunner, Sampler, AlertDispatcher, AttestClient, EngineManager
from attest.assertions import Assertion

engine = EngineManager(engine_path="./attest-engine")
client = AttestClient(engine)

runner = ContinuousEvalRunner(
    client=client,
    assertions=[
        Assertion(type="constraint", spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.10}),
        Assertion(type="content", spec={"check": "non_empty"}),
    ],
    sample_rate=0.1,          # Evaluate 10% of production traces
)

await runner.start()
# Submit traces from your production pipeline
await runner.submit(trace)
```

The engine stores result history in SQLite and computes σ-based drift detection. When assertion scores deviate beyond configured thresholds, Attest fires `drift_alert` notifications to webhooks or Slack. No external infrastructure required. See [`examples/drift-detection/`](https://github.com/attest-framework/attest/tree/main/examples/drift-detection) for a working demo.

## Simulation: Test What Hasn't Happened Yet

Layer 7 is a simulation runtime with mock tools and persona-driven assertions:

```python
from attest import agent, expect
from attest.trace import TraceBuilder
from attest.simulation import MockToolRegistry, mock_tool
from attest.simulation.personas import ADVERSARIAL_USER

@mock_tool("search")
def mock_search(query: str) -> dict:
    return {"results": []}  # Simulate empty results

@agent("search-agent")
def search_agent(builder: TraceBuilder, query: str):
    builder.add_tool_call(name="search", args={"query": query}, result={"results": []})
    builder.set_metadata(total_tokens=100, cost_usd=0.002, latency_ms=500)
    return {"answer": "No results found for your query."}

def test_agent_handles_empty_results(attest):
    with MockToolRegistry() as registry:
        registry.register_decorated(mock_search)
        result = search_agent(query="nonexistent topic")

    chain = (
        expect(result)
        .output_contains("No results")
        .forbidden_tools(["admin_override", "delete_data"])
        .cost_under(0.05)
    )
    attest.evaluate(chain)
```

Test edge cases, adversarial inputs, and failure modes without hitting real APIs. Built-in personas include `FRIENDLY_USER`, `COOPERATIVE_USER`, `CONFUSED_USER`, and `ADVERSARIAL_USER`. See [`examples/simulation/`](https://github.com/attest-framework/attest/tree/main/examples/simulation) for the full working example.

## Multi-Agent Testing

Layer 8 handles the growing reality of multi-agent systems:

```python
from attest import agent, expect, delegate, TraceTree
from attest.trace import TraceBuilder

@agent("orchestrator")
def orchestrator(builder: TraceBuilder, task: str):
    with delegate("researcher") as sub:
        sub.add_tool_call(name="web_search", args={"q": task}, result={"findings": "..."})
        sub.set_output({"summary": "Research complete"})
        sub.set_metadata(total_tokens=200, cost_usd=0.004)
    builder.set_metadata(total_tokens=300, cost_usd=0.008, latency_ms=2000)
    return {"report": "Final report based on research."}

def test_multi_agent(attest):
    result = orchestrator(task="Analyze market trends")

    chain = (
        expect(result)
        .agent_called("researcher")
        .delegation_depth(2)
        .follows_transitions([["orchestrator", "researcher"]])
        .aggregate_cost_under(0.50)
        .aggregate_tokens_under(10000)
    )
    attest.evaluate(chain)

    # Direct trace tree inspection
    tree = TraceTree(root=result.trace)
    assert tree.aggregate_tokens > 0
    assert tree.aggregate_cost > 0
```

Trace trees reconstruct the full delegation chain — which agent called which, with what inputs, producing what outputs. Assert on the graph structure, not just the final answer. See [`examples/multi-agent-workflow/`](https://github.com/attest-framework/attest/tree/main/examples/multi-agent-workflow) for a full multi-agent pipeline example.

## Plugin System

Attest supports third-party plugins via Python entry points:

```toml
# In your plugin's pyproject.toml
[project.entry-points."attest.plugins"]
my-custom-eval = "my_package:MyEvalPlugin"
```

Plugins implement the `AttestPlugin` protocol — define a `name`, `plugin_type`, and `execute(trace, spec)` method returning a `PluginResult`. The `PluginRegistry` discovers and loads them automatically at runtime via the `attest.plugins` entry point group. See [`examples/plugin-system/`](https://github.com/attest-framework/attest/tree/main/examples/plugin-system) for a working demo with custom assertion plugins.

## How Attest Compares

**vs. LangWatch** — LangWatch is an observability platform (BSL 1.1 licensed — effectively proprietary) that also does evaluation and simulation. Its evaluator architecture structurally limits assertions to `input`, `output`, `contexts`, and `expected_output` — trace-level data (span hierarchy, tool call parameters, cost, latency) cannot reach evaluators without a breaking API change. Attest operates directly on full execution traces with assertions over tool ordering, cost budgets, delegation chains, and multi-agent graphs — all in CI, with zero platform infrastructure, under Apache 2.0. The gap between "we can see the traces" and "we can assert over traces in CI" is wider than it appears, and it's architectural.

**vs. DeepEval / Ragas** — These default to LLM-as-judge for everything. Attest exhausts deterministic assertions first, uses LLM judges only as a last resort, and provides soft failure budgets so probabilistic layers don't create CI noise.

**vs. Langfuse / Arize / LangSmith** — Observability platforms that track what happened. Attest is a testing framework that asserts what _should_ happen. Different tools for different jobs — and Attest integrates with all of them via OpenTelemetry.

**vs. Promptfoo** — Config-driven prompt evaluation. Attest is code-first agent testing with full trace-level assertions, simulation, and multi-agent support.

No other open-source tool combines deterministic assertion layers, local embeddings, simulation with fault injection, and multi-agent trace testing in a single framework. Attest is Apache 2.0 with zero infrastructure requirements — no platform to host, no vendor lock-in, no BSL license gotchas.

## Get Started

```bash
pip install attest-ai         # Python
npm install @attest-ai/core   # TypeScript
```

Or scaffold a project:

```bash
python -m attest init
```

This generates an `attest.toml` config and example test file.

**Links:**

- GitHub: [github.com/attest-framework/attest](https://github.com/attest-framework/attest)
- Docs: [attest-framework.github.io/attest](https://attest-framework.github.io/attest/)
- PyPI: [pypi.org/project/attest-ai](https://pypi.org/project/attest-ai/)
- npm: [@attest-ai/core](https://www.npmjs.com/package/@attest-ai/core)

Apache 2.0 licensed. Issues and PRs welcome.

---

_Attest is v0.4.0 today — 8 assertion layers, 11 adapters, continuous eval, drift detection, plugin system, Python + TypeScript SDKs, pytest + vitest integrations. The roadmap includes a Go SDK and cloud dashboard._
