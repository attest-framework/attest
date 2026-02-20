# Attest Quickstart Example

A minimal example demonstrating Attest's assertion DSL for testing AI agents.

## Setup

1. Install dependencies:

```bash
uv sync
```

2. Build the Attest engine binary:

```bash
cd ../..
make engine
```

Ensure the `attest-engine` binary is on your `PATH`:

```bash
export PATH="$(pwd)/dist/engine:$PATH"
```

## Running Tests

Run all tests:

```bash
pytest
```

Run with verbose output:

```bash
pytest -v
```

Run with cost reporting:

```bash
pytest --attest-cost-report
```

## Example Agent

The example includes a simple customer support agent that:

1. Receives a customer refund request
2. Calls `lookup_order` to retrieve order details
3. Calls `process_refund` to issue the refund
4. Returns a confirmation message

## Assertions Demonstrated

The test showcases assertions across all evaluation layers:

- **Layer 1 (Schema)**: Output matches a JSON Schema
- **Layer 2 (Constraints)**: Cost, latency, and token budgets
- **Layer 3 (Trace)**: Tool ordering, required tools, forbidden tools
- **Layer 4 (Content)**: String matching in output messages

## Key Files

- `agent.py` — The customer support agent using the `@agent` decorator
- `test_agent.py` — Tests using the `expect()` DSL with chained assertions
- `conftest.py` — Pytest configuration (imports fixtures from `attest.plugin`)
- `pyproject.toml` — Project dependencies and pytest config

## No API Keys Required

This example uses manual trace construction via the `@agent` decorator and `TraceBuilder`. No LLM API keys are needed.

## Testing Existing Agents

The `@agent` decorator and `TraceBuilder` injection pattern shown here is designed for **synthetic agents** — agents built from scratch with Attest testability in mind. No actual LLM calls are made.

To test **existing agents** from real orchestration frameworks without modifying agent code, use the framework-specific adapters:

- [`examples/langchain-agent/`](../langchain-agent/) — Test LangChain/LangGraph agents via callback handler
- [`examples/google-adk/`](../google-adk/) — Test Google ADK agents via async event stream capture
- [`examples/llamaindex-agent/`](../llamaindex-agent/) — Test LlamaIndex RAG agents via instrumentation dispatcher

Each adapter captures traces externally — the agent code remains untouched.
