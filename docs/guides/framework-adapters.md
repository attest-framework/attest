# Adapter Guide

Adapters turn a model response or an agent run into a `Trace` that Attest can check. Choose a provider adapter for one model call, a framework adapter for the events that framework exposes, or the manual adapter for your own trace-building code.

Maturity of each adapter, generated from `attest-engine capabilities --json`:

<!-- claims:adapter-tiers:start -->
| Adapter | Python | TypeScript |
| --- | --- | --- |
| OpenAI | stable | stable |
| Anthropic | stable | stable |
| Manual | stable | stable |
| Gemini | beta | beta |
| Ollama | beta | beta |
| LangChain | beta | beta |
| CrewAI | beta | — |
| OpenTelemetry | beta | beta |
| Google ADK | experimental | — |
| LlamaIndex | experimental | — |

- **stable**: Covered by the parity and conformance suites; a change in behavior is a breaking change.
- **beta**: Shipped and tested, but the interface or behavior can still change in a minor release.
- **experimental**: Shipped without a stability commitment and without demonstrated demand; it can change or be removed in any release.
<!-- claims:adapter-tiers:end -->

## Adapter Architecture

```text
BaseAdapter                          ← shared utilities
├── BaseProviderAdapter              ← template method for single LLM calls
│   ├── OpenAIAdapter
│   ├── AnthropicAdapter
│   ├── GeminiAdapter
│   └── OllamaAdapter
├── GoogleADKAdapter                 ← framework: ADK event stream
├── LangChainCallbackHandler         ← framework: LangChain callbacks
├── LlamaIndexInstrumentationHandler ← framework: LlamaIndex dispatcher
├── OTelAdapter                      ← framework: OpenTelemetry spans (engine conversion)
├── CrewAIAdapter                     ← framework: output and usage summary only
└── ManualAdapter                    ← utility: builder function
```

**Provider adapters** capture a single LLM request/response cycle. In Python they inherit `BaseProviderAdapter` and override four extraction methods.

**Framework adapters** capture the evidence their framework exposes. LangChain, Google ADK and LlamaIndex can record events across an agent run; CrewAI's adapter records only the final output, token usage and latency, not agent or tool-call steps. Python's provider adapters share `BaseProviderAdapter`; TypeScript adapters implement `TraceAdapter` directly.

```
provider adapter (BaseProviderAdapter)  — one LLM call → one Trace
framework adapter (BaseAdapter)         — one agent run (N LLM calls, M tool calls) → one Trace
```

## Manual adapter: no provider required

Use this when your agent does not have a dedicated adapter. Record what actually ran; a tool-call record is evidence supplied by your code, not proof that Attest executed the tool.

```python
from attest import ManualAdapter, expect

def test_manual_trace(attest):
    builder = ManualAdapter(agent_id="support").create_builder()
    builder.add_tool_call("lookup_order", args={"order_id": "O-1"}, result={"status": "delivered"})
    builder.set_output(message="Order delivered")
    attest.evaluate(expect(builder.build()).required_tools(["lookup_order"]))
```

## Provider Adapters

Every example below runs inside a pytest test that takes the `attest` fixture. `attest.evaluate(chain)` runs every assertion in the chain and fails the test on a hard failure. In 1.1.0, building a chain without evaluating it fails the test; see [verdicts](verdicts.md#every-chain-must-be-evaluated).

### OpenAI

<!-- snippet: skip reason="calls the OpenAI API (needs OPENAI_API_KEY)" -->
```python
from openai import OpenAI
from attest import OpenAIAdapter, AgentResult, expect


def test_explains_recursion(attest):
    client = OpenAI()
    adapter = OpenAIAdapter(agent_id="assistant")

    response = client.chat.completions.create(
        model="gpt-4.1",
        messages=[{"role": "user", "content": "Explain recursion"}],
    )

    trace = adapter.trace_from_response(
        response,
        input_messages=[{"role": "user", "content": "Explain recursion"}],
        cost_usd=0.002,
    )

    result = AgentResult(trace=trace)
    attest.evaluate(expect(result).output_contains("recursion").cost_under(0.10))
```

### Anthropic

<!-- snippet: skip reason="calls the Anthropic API (needs ANTHROPIC_API_KEY)" -->
```python
from anthropic import Anthropic
from attest import AnthropicAdapter, AgentResult, expect


def test_explains_recursion(attest):
    client = Anthropic()
    adapter = AnthropicAdapter(agent_id="claude-agent")

    response = client.messages.create(
        model="claude-sonnet-4-20250514",
        max_tokens=1024,
        messages=[{"role": "user", "content": "Explain recursion"}],
    )

    trace = adapter.trace_from_response(
        response,
        input_messages=[{"role": "user", "content": "Explain recursion"}],
    )

    attest.evaluate(expect(AgentResult(trace=trace)).output_contains("recursion"))
```

### Gemini

<!-- snippet: fragment reason="excerpt: relies on `response`, defined outside the snippet" -->
```python
from attest import GeminiAdapter

adapter = GeminiAdapter(agent_id="gemini-agent")

trace = adapter.trace_from_response(
    response,
    input_messages=[{"role": "user", "content": "Explain recursion"}],
    model="gemini-2.0-flash",  # Gemini model passed via metadata
)
```

### Ollama

<!-- snippet: fragment reason="excerpt: relies on `response_dict`, defined outside the snippet" -->
```python
from attest import OllamaAdapter

adapter = OllamaAdapter(agent_id="local-model")

# Ollama returns a dict, not an object
trace = adapter.trace_from_response(
    response_dict,
    input_messages=[{"role": "user", "content": "Hello"}],
)
```

## Framework Adapters

### LangChain

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai[langchain]>=1.1.0"
```

**Context manager usage:**

<!-- snippet: fragment reason="excerpt: relies on `agent`, defined outside the snippet" -->
```python
from attest import LangChainAdapter, AgentResult, expect


def test_research_agent_searches(attest):
    adapter = LangChainAdapter(agent_id="research-agent")

    with adapter.capture() as handler:
        agent.invoke(
            {"input": "Summarize recent AI research"},
            config={"callbacks": [handler]},
        )

    trace = adapter.trace
    attest.evaluate(expect(AgentResult(trace=trace)).required_tools(["search_web"]))
```

**Direct handler usage:**

<!-- snippet: fragment reason="excerpt: relies on `agent`, defined outside the snippet" -->
```python
from attest import LangChainCallbackHandler

handler = LangChainCallbackHandler(agent_id="my-agent")
result = agent.invoke(input_data, config={"callbacks": [handler]})
trace = handler.build_trace()
```

`build_trace()` raises `RuntimeError` if called more than once.

**Callback mapping:**

| Callback                             | Captured as                           |
| ------------------------------------ | ------------------------------------- |
| `on_chain_start`                     | Agent input                           |
| `on_chain_end`                       | Agent output                          |
| `on_chat_model_start` / `on_llm_end` | `llm_call` step with token counts     |
| `on_tool_start` / `on_tool_end`      | `tool_call` step with args and result |
| `on_tool_error`                      | `tool_call` step with error field     |

### CrewAI

Install with `uv add "attest-ai[crewai]>=1.1.0"`. The adapter is summary-only: it cannot prove that an agent or tool ran from the crew's configuration. Assert on the returned output and recorded usage instead.

<!-- snippet: fragment reason="needs your configured CrewAI crew; kickoff may make model calls" -->
```python
from attest import CrewAIAdapter, expect

def test_crew_reply(attest):
    adapter = CrewAIAdapter(agent_id="support-crew")
    with adapter.capture() as capture:
        output = crew.kickoff()
        trace = capture.trace_from_crew_output(output, crew)
    attest.evaluate(expect(trace).output_contains("refund"))
```

### Google ADK

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai[google-adk]>=1.1.0"
```

**Async capture:**

<!-- snippet: fragment reason="excerpt: relies on `runner`, defined outside the snippet" -->
```python
import pytest
from attest import GoogleADKAdapter, AgentResult, expect


@pytest.mark.asyncio
async def test_weather_agent_looks_up_weather(attest):
    adapter = GoogleADKAdapter(agent_id="root-agent")
    trace = await adapter.capture_async(
        runner=runner,
        user_id="user-123",
        session_id="session-abc",
        message="What is the weather in Paris?",
    )

    await attest.evaluate_async(
        expect(AgentResult(trace=trace)).required_tools(["get_weather"])
    )
```

**From pre-collected events:**

<!-- snippet: fragment reason="excerpt: relies on `GoogleADKAdapter`, defined outside the snippet" -->
```python
trace = GoogleADKAdapter.from_events(
    events=collected_events,
    agent_id="root-agent",
    input_message="What is the weather in Paris?",
)
```

**Event mapping:**

| ADK event field                                | Captured as                     |
| ---------------------------------------------- | ------------------------------- |
| `actions.tool_calls`                           | `tool_call` steps (args)        |
| `actions.tool_results`                         | `tool_call` steps (result)      |
| `actions.transfer_to_agent`                    | `agent_call` step               |
| `usage_metadata.total_token_count`             | accumulated token count         |
| `is_final_response()` + `content.parts[].text` | agent output                    |
| `llm_response.model_version`                   | model metadata (first non-None) |

### LlamaIndex

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai[llamaindex]>=1.1.0"
```

**Context manager usage:**

<!-- snippet: fragment reason="excerpt: relies on `query_engine`, defined outside the snippet" -->
```python
from attest import LlamaIndexInstrumentationHandler, AgentResult, expect


def test_rag_agent_answers_paris(attest):
    with LlamaIndexInstrumentationHandler(agent_id="rag-agent") as handler:
        response = query_engine.query("What is the capital of France?")

    trace = handler.build_trace(
        query="What is the capital of France?",
        response=str(response),
    )

    attest.evaluate(expect(AgentResult(trace=trace)).output_contains("Paris"))
```

**Manual attach/detach:**

<!-- snippet: fragment reason="excerpt: relies on `LlamaIndexInstrumentationHandler`, defined outside the snippet" -->
```python
handler = LlamaIndexInstrumentationHandler(agent_id="rag-agent")
handler.attach()

# ... run queries ...

handler.detach()
trace = handler.build_trace(query="...", response="...")
```

**Event mapping:**

| LlamaIndex event      | Captured as                                |
| --------------------- | ------------------------------------------ |
| `LLMChatStartEvent`   | Buffers model name                         |
| `LLMChatEndEvent`     | `llm_call` step with tokens and tool calls |
| `RetrievalStartEvent` | Buffers query string                       |
| `RetrievalEndEvent`   | `retrieval` step with nodes and scores     |

### OpenTelemetry

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai[otel]>=1.1.0"
```

**From collected spans or an OTLP/JSON export:**

<!-- snippet: fragment reason="excerpt: relies on `collected_spans`, defined outside the snippet" -->
```python
from pathlib import Path

from attest import OTelAdapter

# finished opentelemetry-sdk spans of one trace, e.g. from an InMemorySpanExporter
trace = OTelAdapter.from_spans(collected_spans, agent_id="my-agent")

# an OTLP/JSON export: a pathlib.Path, JSON text (str), bytes or a parsed mapping
trace = OTelAdapter.from_otlp(Path("incident.otlp.json"))
```

TypeScript (`@attest-ai/core`) has the same adapter; both methods are async because they resolve the engine:

<!-- snippet: fragment reason="excerpt: `exporter` is the in-memory span exporter built in the agent's own test setup" -->
```typescript
import { readFileSync } from "node:fs";
import { OTelAdapter } from "@attest-ai/core";

const trace = await OTelAdapter.fromSpans(exporter.getFinishedSpans(), "my-agent");
const fromExport = await OTelAdapter.fromOtlp(readFileSync("incident.otlp.json", "utf8"));
```

The conversion runs in the engine (`attest-engine trace convert`), the same mapping the TypeScript adapter and `attest trace check` use, so every SDK produces the same trace v2 for the same spans. It follows the current OpenTelemetry GenAI semantic conventions: `chat`, `text_completion`, `generate_content` and `embeddings` spans become `llm_call` steps, `execute_tool` spans `tool_call` steps with `args` and `result` from `gen_ai.tool.call.arguments` / `gen_ai.tool.call.result`, nested `invoke_agent` spans `agent_call` steps with handoffs, and top-level `invoke_agent` spans conversation turns. Spans without GenAI attributes are skipped. Malformed input raises `attest.exceptions.OTLPConversionError` (TypeScript: rejects with `OTLPConversionError`) with the engine's message. The full mapping, and `attest trace check` for integrity and coverage, are in [trace-check.md](trace-check.md).

`attest.adapters.otel.to_otel_spans(trace)` exports a trace as OTLP-shaped span dicts under an `invoke_agent` root; `OTelAdapter.from_otel_span_dicts` converts them back.

## TypeScript test runner compatibility

The 1.1.0 Vitest integration supports Vitest 3, 4 and 5. Pass the test context in concurrent tests and when using Vitest before 4.1; the [verdict guide](verdicts.md#vitest) explains the chain guard's lifecycle-hook limits. On a cold machine, run `npx attest doctor` once before the first `npx vitest run`: the initial engine download can exceed Vitest's default five-second test timeout.

## Combining Provider and Framework Adapters

A framework adapter captures LLM calls internally. Use both tiers only when you need separate assertions at each level:

<!-- snippet: fragment reason="excerpt: relies on `response`, defined outside the snippet" -->
```python
from attest import OpenAIAdapter, LangChainAdapter, AgentResult, expect


def test_raw_call_and_agent_run(attest):
    # Provider-level: assert on a raw OpenAI call
    raw_trace = OpenAIAdapter(agent_id="raw-call").trace_from_response(
        response,
        input_messages=[{"role": "user", "content": "Hello"}],
    )
    attest.evaluate(expect(AgentResult(trace=raw_trace)).output_contains("Hello"))

    # Framework-level: assert on the full agent run
    lc_adapter = LangChainAdapter(agent_id="orchestrated-run")
    with lc_adapter.capture() as handler:
        agent.invoke({"input": "Hello"}, config={"callbacks": [handler]})

    attest.evaluate(
        expect(AgentResult(trace=lc_adapter.trace)).required_tools(["lookup_user"])
    )
```

## Writing a Custom Provider Adapter

Inherit `BaseProviderAdapter` and implement 4 extraction methods. The base class handles timestamp resolution, builder creation, LLM step construction, and metadata assembly.

<!-- snippet: harness=adapter_provider -->
```python
from __future__ import annotations

from typing import Any

from attest.adapters import BaseProviderAdapter


class MistralAdapter(BaseProviderAdapter):
    """Adapter for Mistral AI chat completions."""

    def _extract_completion(self, response: Any) -> str:
        return response.choices[0].message.content or ""

    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        return getattr(response, "model", None)

    def _extract_total_tokens(self, response: Any) -> int | None:
        if hasattr(response, "usage") and response.usage:
            return response.usage.total_tokens
        return None

    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        message = response.choices[0].message
        if not hasattr(message, "tool_calls") or not message.tool_calls:
            return []
        return [
            {"name": tc.function.name, "args": {"arguments": tc.function.arguments}}
            for tc in message.tool_calls
        ]
```

**Optional override — `_build_output`:** Customize the trace output dict (default: `{"message": completion_text}`).

<!-- snippet: fragment reason="excerpt: relies on `Any`, defined outside the snippet" -->
```python
def _build_output(
    self, response: Any, completion_text: str, **metadata: Any
) -> dict[str, Any]:
    return {
        "message": completion_text,
        "structured": metadata.get("structured_output", {}),
    }
```

**Optional override — `_extract_input`:** Customize input format (default: `{"messages": input_messages}`).

## Writing a Custom Framework Adapter

Inherit `BaseAdapter` for shared utilities. Framework adapters have diverse input mechanics, so there is no template method — implement your own capture method.

This example maps your own event dictionaries; it does not infer events from a framework's configured agents or tools.

```python
from attest.adapters import BaseAdapter

class WorkflowAdapter(BaseAdapter):
    def trace_from_events(self, events, output):
        builder = self._create_builder()
        for event in events:
            builder.add_tool_call(
                event["name"], args=event["args"], result=event["result"]
            )
        builder.set_output(message=output)
        return builder.build()

trace = WorkflowAdapter(agent_id="support").trace_from_events(
    [{"name": "lookup_order", "args": {"order_id": "O-1"}, "result": {"status": "delivered"}}],
    "Order found",
)
assert trace.steps[0].name == "lookup_order"
assert trace.output["message"] == "Order found"
```

**Shared utilities from `BaseAdapter`:**

| Method                                     | Purpose                                                 |
| ------------------------------------------ | ------------------------------------------------------- |
| `self._create_builder()`                   | Returns a `TraceBuilder` pre-configured with `agent_id` |
| `self._now_ms()`                           | Current wall-clock time in milliseconds                 |
| `self._resolve_timestamps(started, ended)` | Fills `None` values with current time                   |

## Base Class Reference

```python
from attest.adapters import BaseAdapter, BaseProviderAdapter
```

| Class                 | Use case                            | Abstract methods                                                                        |
| --------------------- | ----------------------------------- | --------------------------------------------------------------------------------------- |
| `BaseAdapter`         | Framework adapters, custom adapters | None (concrete)                                                                         |
| `BaseProviderAdapter` | Single LLM call adapters            | `_extract_completion`, `_extract_model`, `_extract_total_tokens`, `_extract_tool_calls` |

The `TraceAdapter`, `ProviderAdapter` and `FrameworkAdapter` Protocols were removed from the Python SDK in 1.0. Subclass `BaseAdapter` or `BaseProviderAdapter` instead.
