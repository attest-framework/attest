# Adapters Reference

Integrations with LLM providers, frameworks, and observability platforms.

## Overview

Adapters provide automatic instrumentation and integration with external services. Attest includes 10+ built-in adapters covering the most common use cases.

## LLM Providers

### OpenAI

Auto-instrument OpenAI API calls with cost and latency tracking.

```python
from attest.adapters import openai

# Adapter automatically patches OpenAI client
client = openai.create_client()

result = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "..."}]
)

# Result includes trace, cost, latency
expect(result).output_contains("...").cost_under(0.05)
```

**Supported models:**
- `gpt-4o`
- `gpt-4o-mini`
- `gpt-4-turbo`
- `gpt-3.5-turbo`

### Anthropic

Test Claude models with automatic instrumentation.

```python
from attest.adapters import anthropic

client = anthropic.create_client()

result = client.messages.create(
    model="claude-3-sonnet",
    messages=[{"role": "user", "content": "..."}],
    max_tokens=1024
)

expect(result).output_contains("...").trace_contains_model("claude-3-sonnet")
```

**Supported models:**
- `claude-3-opus`
- `claude-3-sonnet`
- `claude-3-haiku`

### Google Gemini

Test Google Gemini with cost tracking.

```python
from attest.adapters import gemini

client = gemini.create_client()

result = client.generate_content(
    model="gemini-2.0-flash",
    contents="..."
)

expect(result).cost_under(0.01)
```

### Ollama

Test local models running on Ollama.

```python
from attest.adapters import ollama

# Connects to local Ollama instance (localhost:11434)
result = ollama.generate(
    model="mistral",
    prompt="..."
)

# No API calls, latency only includes local inference
expect(result).latency_under(5000)
```

### Hugging Face

Test models via Hugging Face inference API.

```python
from attest.adapters import huggingface

result = huggingface.generate(
    model="meta-llama/Llama-2-7b-chat",
    text="...",
    api_token="hf_..."
)

expect(result).output_contains("...")
```

## Framework Adapters

### LangChain

Auto-instrument LangChain agents and chains.

```python
from attest.adapters import langchain
from langchain_openai import ChatOpenAI
from langchain.agents import create_react_agent

# Setup agent
llm = ChatOpenAI(model="gpt-4o-mini")
tools = [...]
agent = create_react_agent(llm, tools)

# Attest automatically captures trace
result = agent.invoke({"input": "question"})

expect(result).trace_contains_tool("google_search").cost_under(0.10)
```

**Instruments:**
- Chains
- Agents (ReAct, etc.)
- Tool usage
- Model calls
- Token costs

### CrewAI

Test multi-agent systems built with CrewAI.

```python
from attest.adapters import crewai
from crewai import Agent, Task, Crew

agent = Agent(role="Research", goal="Find information", llm=...)
task = Task(description="Research...", agent=agent)
crew = Crew(agents=[agent], tasks=[task])

result = crew.kickoff()

# Trace includes all agent interactions
expect(result).trace_tree_valid().all_agents_passed()
```

### LlamaIndex

Test query engines and indexing pipelines.

```python
from attest.adapters import llamaindex
from llama_index.core import VectorStoreIndex

index = VectorStoreIndex.from_documents(docs)
engine = index.as_query_engine()

result = engine.query("question")

expect(result).output_contains("...").latency_under(3000)
```

### Haystack

Test Haystack pipelines.

```python
from attest.adapters import haystack
from haystack.pipelines import Pipeline

pipeline = Pipeline()
# ... configure pipeline ...

result = pipeline.run({"prompt": "..."})

expect(result).output_contains("...")
```

## Observability Adapters

### OpenTelemetry

Export Attest traces to OpenTelemetry for monitoring.

```python
from attest.adapters import otel
from opentelemetry.exporter.jaeger.thrift import JaegerExporter

otel.setup_exporter(
    JaegerExporter(agent_host_name="localhost", agent_port=6831)
)

result = agent.run("...")

# Trace automatically exported to Jaeger
expect(result).cost_under(0.05)
```

## Custom Adapters

Write custom adapters for unsupported frameworks.

```python
from attest.adapters import BaseAdapter

class MyFrameworkAdapter(BaseAdapter):
    def capture_trace(self, fn, args, kwargs):
        """Capture trace from framework calls."""
        result = fn(*args, **kwargs)

        # Extract trace, cost, latency from framework
        return {
            'output': result.text,
            'cost': calculate_cost(result),
            'latency_ms': result.duration,
            'trace': extract_trace(result)
        }

# Register adapter
attest.register_adapter('my_framework', MyFrameworkAdapter())
```

See [Writing a Framework Adapter](../../tutorials/writing-a-framework-adapter.md) for complete guide.

## Provider Comparison

| Provider | Cost Tracking | Latency | Trace | Free Tier |
|----------|---------------|---------|-------|-----------|
| OpenAI | ✅ | ✅ | ✅ | ✅ |
| Anthropic | ✅ | ✅ | ✅ | ✅ |
| Gemini | ✅ | ✅ | ✅ | ✅ |
| Ollama | ❌ | ✅ | ✅ | ✅ |
| Hugging Face | ⚠️ | ✅ | ✅ | ✅ |

## Configuration

### Global Settings

```python
from attest import config

# Set default provider
config.set_provider("openai")

# Set default model for judges
config.set_model("gpt-4o-mini")

# Set API timeout
config.set_timeout(30)

# Set maximum retries
config.set_max_retries(3)
```

### Per-Adapter Settings

```python
from attest.adapters import openai

# Custom OpenAI configuration
openai.configure(
    api_key="sk-...",
    organization="org-...",
    timeout=60,
    max_retries=5
)
```

## Environment Variables

Most adapters read from environment variables:

```bash
# OpenAI
export OPENAI_API_KEY="sk-..."
export OPENAI_ORG_ID="org-..."

# Anthropic
export ANTHROPIC_API_KEY="sk-ant-..."

# Google
export GOOGLE_API_KEY="AIza..."

# Hugging Face
export HF_API_KEY="hf_..."

# Ollama
export OLLAMA_BASE_URL="http://localhost:11434"
```

## Error Handling

Adapters provide detailed error messages:

```python
from attest import expect, AdapterError

try:
    result = agent.run("...")
except AdapterError as e:
    print(f"Adapter error: {e.message}")
    print(f"Provider: {e.provider}")
    print(f"Suggestion: {e.suggestion}")
```

## Rate Limiting

Adapters handle rate limiting automatically:

```python
from attest.adapters import openai

# Automatic backoff and retry
result = openai.create_completion(...)  # Retries on rate limit

# Custom rate limiting
openai.configure(requests_per_minute=60)
```

## Supported Models

### OpenAI

- GPT-4o (`gpt-4o`)
- GPT-4o-mini (`gpt-4o-mini`)
- GPT-4 Turbo (`gpt-4-turbo-preview`)
- GPT-3.5 Turbo (`gpt-3.5-turbo`)

### Anthropic

- Claude 3 Opus (`claude-3-opus-20240229`)
- Claude 3 Sonnet (`claude-3-sonnet-20240229`)
- Claude 3 Haiku (`claude-3-haiku-20240307`)

### Google Gemini

- Gemini 2.0 Flash (`gemini-2.0-flash`)
- Gemini 1.5 Pro (`gemini-1.5-pro`)
- Gemini 1.5 Flash (`gemini-1.5-flash`)

### Local Models (Ollama)

- Mistral
- Llama 2
- Phi
- Neural Chat
- And 100+ more

## Related

- [Python SDK Overview](index.md) — Core concepts
- [Expect DSL](expect.md) — Assertion methods
- [Writing a Framework Adapter](../../tutorials/writing-a-framework-adapter.md) — Build custom adapters
