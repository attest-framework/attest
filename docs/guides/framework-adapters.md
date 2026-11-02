# Adapter Guide

Attest adapters capture LLM interactions as `Trace` objects. Two adapter tiers exist, backed by a shared class hierarchy.

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
└── ManualAdapter                    ← utility: builder function
```

**Provider adapters** capture a single LLM request/response cycle. They inherit `BaseProviderAdapter` and override 4 extraction methods.

**Framework adapters** capture a complete agent run (multiple LLM calls, tool calls, sub-agent delegations). They inherit `BaseAdapter` for shared utilities but implement their own capture mechanics.

```
provider adapter (BaseProviderAdapter)  — one LLM call → one Trace
framework adapter (BaseAdapter)         — one agent run (N LLM calls, M tool calls) → one Trace
```

## Provider Adapters

### OpenAI

```python
from openai import OpenAI
from attest import OpenAIAdapter, AgentResult, expect

client = OpenAI()
adapter = OpenAIAdapter(agent_id="assistant")

response = client.chat.completions.create(
    model="gpt-4.1",
    messages=[{"role": "user", "content": "Explain recursion"}],
)

trace = adapter.trace_from_response(
    response,
    input_messages=[{"role": "user", "content": "Explain recursion"}],
)

result = AgentResult(trace=trace)
expect(result).output_contains("recursion").cost_under(0.10)
```

### Anthropic

```python
from anthropic import Anthropic
from attest import AnthropicAdapter, AgentResult, expect

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
```

### Gemini

```python
from attest import GeminiAdapter

adapter = GeminiAdapter(agent_id="gemini-agent")

trace = adapter.trace_from_response(
    response,
    input_messages=[{"role": "user", "content": "Explain recursion"}],
    model="gemini-2.0-flash",  # Gemini model passed via metadata
)
```

> **Migration note:** The `input_text` parameter is deprecated. Use `input_messages=[{"role": "user", "content": text}]` instead.

### Ollama

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

```bash
uv add 'attest-ai[langchain]>=1.0.0rc1'
```

**Context manager usage:**

```python
from attest import LangChainAdapter, AgentResult, expect

adapter = LangChainAdapter(agent_id="research-agent")

with adapter.capture() as handler:
    result = agent.invoke(
        {"input": "Summarize recent AI research"},
        config={"callbacks": [handler]},
    )

trace = adapter.trace
expect(AgentResult(trace=trace)).tool_called("search_web")
```

**Direct handler usage:**

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

### Google ADK

```bash
uv add 'attest-ai[google-adk]>=1.0.0rc1'
```

**Async capture:**

```python
from attest import GoogleADKAdapter, AgentResult, expect

adapter = GoogleADKAdapter(agent_id="root-agent")
trace = await adapter.capture_async(
    runner=runner,
    user_id="user-123",
    session_id="session-abc",
    message="What is the weather in Paris?",
)

expect(AgentResult(trace=trace)).tool_called("get_weather")
```

**From pre-collected events:**

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

```bash
uv add 'attest-ai[llamaindex]>=1.0.0rc1'
```

**Context manager usage:**

```python
from attest import LlamaIndexInstrumentationHandler, AgentResult, expect

with LlamaIndexInstrumentationHandler(agent_id="rag-agent") as handler:
    response = query_engine.query("What is the capital of France?")

trace = handler.build_trace(
    query="What is the capital of France?",
    response=str(response),
)

expect(AgentResult(trace=trace)).output_contains("Paris")
```

**Manual attach/detach:**

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

```bash
uv add 'attest-ai[otel]>=1.0.0rc1'
```

**From collected spans or an OTLP/JSON export:**

```python
from attest import OTelAdapter

# finished opentelemetry-sdk spans of one trace, e.g. from an InMemorySpanExporter
trace = OTelAdapter.from_spans(collected_spans, agent_id="my-agent")

# an OTLP/JSON export (path, JSON text, bytes or parsed mapping)
trace = OTelAdapter.from_otlp("incident.otlp.json")
```

The conversion runs in the engine (`attest-engine trace convert`), the same mapping the TypeScript adapter and `attest trace check` use, so every SDK produces the same trace v2 for the same spans. It follows the current OpenTelemetry GenAI semantic conventions: `chat`, `text_completion`, `generate_content` and `embeddings` spans become `llm_call` steps, `execute_tool` spans `tool_call` steps with `args` and `result` from `gen_ai.tool.call.arguments` / `gen_ai.tool.call.result`, nested `invoke_agent` spans `agent_call` steps with handoffs, and top-level `invoke_agent` spans conversation turns. Spans without GenAI attributes are skipped. Malformed input raises `attest.exceptions.OTLPConversionError` with the engine's message. The full mapping, and `attest trace check` for integrity and coverage, are in [trace-check.md](trace-check.md).

`attest.adapters.otel.to_otel_spans(trace)` exports a trace as OTLP-shaped span dicts under an `invoke_agent` root; `OTelAdapter.from_otel_span_dicts` converts them back.

## Combining Provider and Framework Adapters

A framework adapter captures LLM calls internally. Use both tiers only when you need separate assertions at each level:

```python
from attest import OpenAIAdapter, LangChainAdapter, AgentResult, expect

# Provider-level: assert on a raw OpenAI call
raw_trace = OpenAIAdapter(agent_id="raw-call").trace_from_response(
    response,
    input_messages=[{"role": "user", "content": "Hello"}],
)
expect(AgentResult(trace=raw_trace)).output_contains("Hello")

# Framework-level: assert on the full agent run
lc_adapter = LangChainAdapter(agent_id="orchestrated-run")
with lc_adapter.capture() as handler:
    agent.invoke({"input": "Hello"}, config={"callbacks": [handler]})

expect(AgentResult(trace=lc_adapter.trace)).tool_called("lookup_user")
```

## Writing a Custom Provider Adapter

Inherit `BaseProviderAdapter` and implement 4 extraction methods. The base class handles timestamp resolution, builder creation, LLM step construction, and metadata assembly.

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

```python
from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.adapters import BaseAdapter


class CrewAIAdapter(BaseAdapter):
    """Adapter for CrewAI task execution."""

    def trace_from_crew(self, crew_output: Any, **metadata: Any) -> Trace:
        builder = self._create_builder()
        total_tokens = 0

        for task_output in crew_output.tasks_output:
            agent_name = task_output.agent
            completion = task_output.raw

            builder.add_llm_call(
                name=agent_name,
                result={"completion": completion},
            )

            for tool_use in getattr(task_output, "tool_uses", []):
                builder.add_tool_call(
                    name=tool_use.tool_name,
                    args=tool_use.args,
                    result=tool_use.result,
                    agent_id=agent_name,
                )
                total_tokens += getattr(tool_use, "tokens", 0)

        builder.set_output(message=crew_output.raw)
        builder.set_metadata(
            total_tokens=total_tokens if total_tokens > 0 else None,
            cost_usd=metadata.get("cost_usd"),
        )

        return builder.build()
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
