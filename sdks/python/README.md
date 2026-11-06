# attest-ai

Test framework for AI agents. Deterministic assertions (schema validation, cost constraints, trace ordering, content matching) over agent execution traces.

## Install

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai>=1.0.1" pytest
```

With LLM provider support:

<!-- snippet: skip reason="installs packages or runs npm tooling (network)" -->
```bash
uv add "attest-ai[openai]>=1.0.1"      # OpenAI
uv add "attest-ai[anthropic]>=1.0.1"   # Anthropic
uv add "attest-ai[gemini]>=1.0.1"      # Google Gemini
uv add "attest-ai[ollama]>=1.0.1"      # Ollama (local)
uv add "attest-ai[all]>=1.0.1"         # All providers
```

## Quick start

Save as `test_refund_agent.py`. The `attest` fixture starts the engine, and `attest.evaluate(chain)` runs every assertion in the chain and fails the test on a hard failure. A chain that is never passed to `evaluate()` checks nothing.

```python
from attest import AgentResult, TraceBuilder, expect


def refund_agent_result() -> AgentResult:
    """Stand-in for your agent: build the trace it produced."""
    trace = (
        TraceBuilder("refund-agent")
        .add_tool_call("lookup_order", args={"order_id": "ORD-123"}, result={"status": "paid"})
        .add_tool_call("process_refund", args={"order_id": "ORD-123"}, result={"refund_id": "R-1"})
        .set_output(message="Your refund R-1 is on its way.", structured={"refund_id": "R-1"})
        .set_metadata(cost_usd=0.01, latency_ms=800)
        .build()
    )
    return AgentResult(trace=trace)


def test_refund_agent(attest):
    result = refund_agent_result()
    attest.evaluate(
        expect(result)
        # Layer 1: schema validation
        .output_matches_schema({"type": "object", "required": ["refund_id"]})
        # Layer 2: cost and latency constraints
        .cost_under(0.05)
        .latency_under(5000)
        # Layer 3: trace structure
        .tools_called_in_order(["lookup_order", "process_refund"])
        .no_tool_loops("lookup_order", max_repetitions=3)
        # Layer 4: content
        .output_contains("refund")
        .output_not_contains("sorry")
    )
```

<!-- snippet: setup=project -->
```bash
pytest test_refund_agent.py --attest-report=report.json
```

Change one assertion, for example `.output_contains("refund")` to `.output_contains("chargeback")`, and the test fails with a diagnostic for that assertion.

## Helpers

- `TraceBuilder.set_metadata(...)` merges: fields given in a later call are added, and fields it leaves out keep their value. `set_metadata(cost_usd=0.5)` followed by `set_metadata(latency_ms=100)` keeps both, so `cost_under(0.05)` fails on the cost.
- `@agent("name")` wraps a function with a `builder` parameter that Attest injects. A string return becomes the output `{"message": <str>}`, so `output_contains` works on a plain agent; a dict return is the output as is. Positional arguments are forwarded: with `def my_agent(builder, user_message)`, both `my_agent("hello")` and `my_agent(user_message="hello")` work. `adapter=` converts a provider adapter's return value and raises `TypeError` at decoration for an adapter that cannot convert.
- When the engine cannot be found or downloaded (offline, or behind a blocking proxy), pytest ends the run with one error line that names `ATTEST_ENGINE_PATH`, without a traceback. Build an engine with `make engine` and point `ATTEST_ENGINE_PATH` at it to run offline.

## Pytest integration

Attest registers as a pytest plugin automatically:

<!-- snippet: fragment reason="`/path/to/attest-engine` is a placeholder" -->
```bash
pytest tests/ --attest-engine=/path/to/attest-engine
```

## Links

- [Repository](https://github.com/attest-framework/attest)
- [Contributing](https://github.com/attest-framework/attest/blob/main/CONTRIBUTING.md)
- [License](https://github.com/attest-framework/attest/blob/main/LICENSE) (Apache-2.0)
