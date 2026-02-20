# Python SDK Reference

Complete API documentation for the Attest Python SDK.

## Overview

The Attest Python SDK provides a fluent API for testing AI agents and systems. The core components are:

- **Expect DSL** — Fluent assertions across 8 layers of validation
- **Adapters** — Integration with LLMs, frameworks, and observability platforms
- **Simulation** — Multi-agent scenario testing and fault injection
- **Trace inspection** — Access execution traces and reconstruct trace trees

## Core Modules

### Expect DSL

The main entry point for writing assertions:

```python
from attest import expect

result = agent.run("question")
(expect(result)
  .output_contains("expected text")
  .cost_under(0.05)
  .passes_judge("Is this correct?"))
```

See the [Expect DSL reference](expect.md) for complete method documentation.

### Adapters

Integrations with LLMs and frameworks:

```python
from attest.adapters import openai, langchain, anthropic

# Use built-in adapters for auto-instrumentation
agent = langchain.create_agent(...)
result = agent.run("question")
```

See the [Adapters reference](adapters.md) for all available providers.

### Simulation Runtime

Test multi-agent scenarios with repeats and fault injection:

```python
from attest import simulate

scenario = simulate.scenario()
scenario.add_agent(agent1)
scenario.add_agent(agent2)
results = scenario.run(repeat=3, inject_faults=True)

(expect(results)
  .all_pass()
  .success_rate_above(0.95))
```

### Trace Inspection

Access execution traces and reconstruct trace trees:

```python
from attest import trace_tree

result = agent.run("question")
tree = trace_tree.build(result.trace)

# Inspect tree structure
print(tree.root.model)
for call in tree.root.tool_calls:
    print(f"Tool: {call.name}")
```

## Assertion Layers

Attest assertions work across 8 layers:

| Layer | Methods | What it validates |
|-------|---------|------------------|
| **1. Schema** | `matches_schema()` | JSON schema validation |
| **2. Constraints** | `cost_under()`, `latency_under()` | Performance metrics |
| **3. Trace** | `trace_contains_model()`, `trace_contains_tool()` | Execution path |
| **4. Content** | `output_contains()`, `output_matches()` | Text content |
| **5. Embedding** | `semantically_similar_to()` | Semantic meaning |
| **6. LLM Judge** | `passes_judge()` | Domain-specific evaluation |
| **7. Trace Tree** | `trace_tree_valid()`, `tool_calls_valid()` | Execution structure |
| **8. Simulation** | `all_pass()`, `success_rate_above()` | Multi-agent scenarios |

## Common Patterns

### Soft Failures

Continue testing after failures to collect all issues:

```python
from attest import soft_fail

with soft_fail():
    expect(result).output_contains("hello")  # May fail
    expect(result).cost_under(0.01)          # May fail
    # Both will run, collecting all failures
```

### Custom Judges

Use LLM evaluation for semantic correctness:

```python
(expect(result)
  .passes_judge(
    prompt="Is the response grammatically correct?",
    model="gpt-4o",
    scoring="binary"  # binary, scale_0_10, or enum
  ))
```

### Framework Integration

Test agents built with popular frameworks:

```python
from attest.adapters import langchain, crewai, llamaindex

# LangChain agents
agent = langchain.create_agent(...)

# CrewAI tasks
task = crewai.create_task(...)

# LlamaIndex query engines
engine = llamaindex.create_query_engine(...)
```

## API Documentation

- [Expect DSL](expect.md) — Complete method reference for assertions
- [Adapters](adapters.md) — Provider integrations and framework adapters

## Examples

### Simple Assertion

```python
from attest import expect

result = agent.run("What is 2 + 2?")

expect(result).output_contains("4")
```

### Chained Assertions

```python
(expect(result)
  .output_contains("4")
  .cost_under(0.05)
  .latency_under(2000)
  .passes_judge("Is the answer correct?"))
```

### Adapter-Specific

```python
from attest.adapters import openai

# With OpenAI adapter
result = openai.create_completion(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "..."}]
)

(expect(result)
  .output_contains("...")
  .trace_contains_model("gpt-4o-mini"))
```

### Simulation with Multi-Agent

```python
from attest import simulate

scenario = simulate.scenario()
scenario.add_agent(agent1, role="researcher")
scenario.add_agent(agent2, role="reviewer")
results = scenario.run(repeat=5)

(expect(results)
  .all_pass()
  .success_rate_above(0.95)
  .avg_latency_under(3000))
```

## Configuration

Set global configuration for all tests:

```python
from attest import config

config.set_model("gpt-4o-mini")  # Default LLM for judges
config.set_provider("openai")     # Default provider
config.set_timeout(30)            # Global timeout (seconds)
```

## Troubleshooting

**"API key not found"**

Set environment variables for your provider:

```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
```

**"Assertion failed unexpectedly"**

Inspect the result details:

```python
print(result.output)           # Agent output
print(result.cost)             # Token cost
print(result.latency_ms)       # Execution time
print(result.trace)            # Full execution trace
```

**"Custom judge not available"**

Ensure the judge model is accessible:

```python
expect(result).passes_judge(
    prompt="...",
    model="gpt-4o",  # Verify this model is available
    fallback="gpt-4o-mini"  # Fallback if unavailable
)
```

## Advanced Topics

- **Trace tree reconstruction** — Build and inspect trace trees from execution logs
- **Custom adapters** — Write adapters for unsupported frameworks
- **Simulation scenarios** — Create complex multi-agent testing scenarios
- **Fault injection** — Test agent resilience with injected failures

See [Writing a Framework Adapter](../../tutorials/writing-a-framework-adapter.md) for advanced topics.
