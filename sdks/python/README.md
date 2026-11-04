# attest-ai

Test framework for AI agents. Deterministic assertions (schema validation, cost constraints, trace ordering, content matching) over agent execution traces.

## Install

```bash
uv add "attest-ai>=1.0.0"
```

With LLM provider support:

```bash
uv add "attest-ai[openai]>=1.0.0"      # OpenAI
uv add "attest-ai[anthropic]>=1.0.0"   # Anthropic
uv add "attest-ai[gemini]>=1.0.0"      # Google Gemini
uv add "attest-ai[ollama]>=1.0.0"      # Ollama (local)
uv add "attest-ai[all]>=1.0.0"         # All providers
```

## Quick start

```python
import attest
from attest import expect

result = attest.AgentResult(
    trace=trace,  # captured from your agent
    assertion_results=[],
)

# Layer 1: Schema validation
expect(result).output_matches_schema({"type": "object", "required": ["refund_id"]})

# Layer 2: Cost & performance constraints
expect(result).cost_under(0.05)
expect(result).latency_under(5000)

# Layer 3: Trace structure
expect(result).tools_called_in_order(["lookup_order", "process_refund"])
expect(result).no_tool_loops(max_iterations=3)

# Layer 4: Content assertions
expect(result).output_contains("refund")
expect(result).output_not_contains("sorry")
```

## Pytest integration

Attest registers as a pytest plugin automatically:

```bash
pytest tests/ --attest-engine=/path/to/attest-engine
```

## Links

- [Repository](https://github.com/attest-framework/attest)
- [Contributing](https://github.com/attest-framework/attest/blob/main/CONTRIBUTING.md)
- [License](https://github.com/attest-framework/attest/blob/main/LICENSE) (Apache-2.0)
