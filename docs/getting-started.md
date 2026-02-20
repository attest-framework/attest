# Getting Started

Get up and running with Attest in 5 minutes.

## Installation

=== "Python + uv"

    ```bash
    uv add attest-ai
    ```

=== "Python + pip"

    ```bash
    pip install attest-ai
    ```

=== "Node.js + npm"

    ```bash
    npm install @attest-ai/core
    ```

=== "Node.js + pnpm"

    ```bash
    pnpm add @attest-ai/core
    ```

## Write Your First Test

### Step 1: Create an Agent

Start with a simple agent that uses OpenAI:

=== "Python"

    ```python
    from openai import OpenAI

    client = OpenAI()

    def my_agent(question: str) -> str:
        response = client.chat.completions.create(
            model="gpt-4o-mini",
            messages=[{"role": "user", "content": question}]
        )
        return response.choices[0].message.content
    ```

=== "Node.js"

    ```typescript
    import OpenAI from "openai";

    const client = new OpenAI();

    async function myAgent(question: string): Promise<string> {
      const response = await client.chat.completions.create({
        model: "gpt-4o-mini",
        messages: [{ role: "user", content: question }]
      });
      return response.choices[0].message.content || "";
    }
    ```

### Step 2: Add Assertions

Now test the agent with Attest assertions:

=== "Python"

    ```python
    from attest import expect

    # Run the agent
    result = my_agent("What is 2 + 2?")

    # Assert the output
    (expect(result)
      .output_contains("4")
      .cost_under(0.05))
    ```

=== "Node.js"

    ```typescript
    import { expect } from "@attest-ai/core";

    const result = await myAgent("What is 2 + 2?");

    expect(result)
      .output_contains("4")
      .cost_under(0.05);
    ```

### Step 3: Run Tests

=== "Python"

    ```bash
    python test_agent.py
    ```

=== "Node.js"

    ```bash
    node test_agent.js
    ```

If all assertions pass, you'll see:

```
✓ All assertions passed
```

If an assertion fails, you'll see details:

```
✗ Assertion failed: output_contains("goodbye")
  Expected output to contain: goodbye
  Actual output: hello world
```

## Understanding Assertions

Attest uses a fluent API where you chain assertions. Each assertion validates a different aspect:

| Layer | Example | What it checks |
|-------|---------|----------------|
| Schema | `.matches_schema({"type": "object"})` | Output structure |
| Constraints | `.cost_under(0.10)`, `.latency_under(2000)` | Performance metrics |
| Trace | `.trace_contains_model("gpt-4o-mini")` | Execution path |
| Content | `.output_contains("hello")` | Text content |
| Embedding | `.semantically_similar_to("greeting")` | Semantic meaning |
| LLM Judge | `.passes_judge("Is this polite?")` | Domain-specific eval |
| Trace Tree | `.trace_tree_valid()` | Structure of full trace |
| Simulation | `.simulation_passes()` | Multi-agent scenario |

## Next Steps

- **[API Reference](reference/python/index.md)** — Explore all assertion methods
- **[Adapters](reference/python/adapters.md)** — Learn about provider integration
- **[Migration Guides](migration/from-deepeval.md)** — Upgrade from other frameworks
- **[Write a Adapter](tutorials/writing-a-framework-adapter.md)** — Build a custom integration

## Common Patterns

### Multiple Assertions

Chain multiple assertions on one result:

=== "Python"

    ```python
    (expect(result)
      .output_contains("success")
      .cost_under(0.05)
      .latency_under(3000)
      .passes_judge("Is the response helpful?"))
    ```

### Soft Failures

Continue testing after failures to see all issues:

=== "Python"

    ```python
    from attest import expect, soft_fail

    with soft_fail():
        (expect(result)
          .output_contains("hello")  # May fail
          .cost_under(0.01)           # May fail
          .passes_judge("..."))       # Will still run
    ```

### Adapter Integration

Use built-in adapters for framework-specific features:

=== "Python"

    ```python
    from attest.adapters import langchain

    # Agent built with LangChain
    from langchain_openai import ChatOpenAI
    from langchain.agents import create_react_agent

    agent = create_react_agent(...)
    result = agent.invoke({"input": "..."})

    # Attest auto-captures trace
    expect(result).output_contains("...").trace_contains_tool("google_search")
    ```

## Troubleshooting

**"API key not found"**

Set your provider's API key as an environment variable:

```bash
export OPENAI_API_KEY="sk-..."
```

**"Assertion failed but I expected it to pass"**

Check the actual output:

=== "Python"

    ```python
    print(result.output)  # See what the agent actually returned
    print(result.cost)    # Check cost and latency
    print(result.trace)   # Inspect execution trace
    ```

**"Tests running slow"**

Use local models with Ollama to reduce latency:

=== "Python"

    ```python
    from attest.adapters import ollama

    # Runs locally, no API calls
    result = ollama_agent("What is 2 + 2?")
    ```

## Learn More

Check the [API reference](reference/python/index.md) for complete method documentation and the [migration guides](migration/from-deepeval.md) if you're upgrading from another framework.
