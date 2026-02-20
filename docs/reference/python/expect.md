# Expect DSL

Fluent assertion API for testing AI agent outputs across 8 validation layers.

## Overview

The `ExpectChain` class provides a fluent interface for chaining assertions. Each assertion returns the chain for further chaining.

```python
from attest import expect

result = agent.run("question")

# Assertions can be chained
(expect(result)
  .output_contains("expected")
  .cost_under(0.05)
  .latency_under(2000)
  .passes_judge("Is correct?"))
```

## Basic Usage

### Creating an Expect Chain

```python
from attest import expect

result = agent_function()
chain = expect(result)
```

The result object contains:

- `output` — The agent's text output
- `cost` — Token cost in dollars
- `latency_ms` — Execution time in milliseconds
- `trace` — Full execution trace
- `metadata` — Custom metadata

## Layer 1: Schema Validation

Validate output structure against a schema.

```python
expect(result).matches_schema({
    "type": "object",
    "properties": {
        "name": {"type": "string"},
        "age": {"type": "number"}
    },
    "required": ["name", "age"]
})
```

## Layer 2: Constraints

Check performance metrics and limits.

### Cost Constraints

```python
# Cost in dollars
expect(result).cost_under(0.10)
expect(result).cost_equals(0.05)
expect(result).cost_between(0.01, 0.10)
```

### Latency Constraints

```python
# Latency in milliseconds
expect(result).latency_under(5000)
expect(result).latency_equals(1000)
expect(result).latency_between(100, 5000)
```

## Layer 3: Trace Content

Inspect what models and tools the agent used.

```python
# Check model usage
expect(result).trace_contains_model("gpt-4o-mini")
expect(result).trace_contains_model("claude-3-sonnet")

# Check tool usage
expect(result).trace_contains_tool("google_search")
expect(result).trace_contains_tool("calculator")

# Verify no unexpected tools
expect(result).trace_contains_only_tools(["calculator", "wikipedia"])
```

## Layer 4: Content Matching

Check the output text.

### Exact Content

```python
# Contains substring
expect(result).output_contains("hello")

# Does not contain
expect(result).output_not_contains("error")

# Exact match
expect(result).output_equals("exact output")

# Case insensitive
expect(result).output_contains("HELLO", case_sensitive=False)
```

### Pattern Matching

```python
import re

# Regex pattern
expect(result).output_matches(r"^\d{4}-\d{2}-\d{2}$")  # Date format

# Contains all substrings
expect(result).output_contains_all(["hello", "world"])

# Contains any substring
expect(result).output_contains_any(["yes", "correct"])

# Starts/ends with
expect(result).output_starts_with("The")
expect(result).output_ends_with("?")
```

### Word Count

```python
expect(result).word_count_equals(100)
expect(result).word_count_between(50, 200)
expect(result).word_count_under(500)
```

## Layer 5: Semantic Similarity

Check semantic meaning using embeddings.

```python
# Semantically similar to reference text
expect(result).semantically_similar_to(
    "This is a greeting",
    threshold=0.85
)

# Semantically different from reference
expect(result).semantically_different_from(
    "This is an error message",
    threshold=0.85
)
```

## Layer 6: LLM-as-Judge

Use an LLM to evaluate domain-specific correctness.

### Basic Judge

```python
expect(result).passes_judge(
    prompt="Is this response helpful?"
)
```

### With Custom Model

```python
expect(result).passes_judge(
    prompt="Is the math correct?",
    model="gpt-4o",
    scoring="binary"  # binary, scale_0_10, or enum
)
```

### Multiple Judges

```python
expect(result).passes_judges([
    ("Is this helpful?", "gpt-4o-mini"),
    ("Is this accurate?", "gpt-4o"),
])
```

### Judge with Rubric

```python
expect(result).passes_judge(
    prompt="Grade this response",
    rubric={
        "clarity": "Is the explanation clear?",
        "accuracy": "Are the facts correct?",
        "completeness": "Does it answer fully?"
    },
    threshold=0.8
)
```

## Layer 7: Trace Tree Validation

Validate the structure of the execution trace.

```python
# Verify trace structure is valid
expect(result).trace_tree_valid()

# Verify specific tool calls
expect(result).tool_calls_valid()

# Check tool call count
expect(result).tool_call_count_equals(3)
expect(result).tool_call_count_between(1, 5)

# Verify no infinite loops
expect(result).trace_depth_under(10)
```

## Layer 8: Simulation Results

Validate multi-agent scenario results.

```python
from attest import simulate

scenario = simulate.scenario()
scenario.add_agent(agent1)
scenario.add_agent(agent2)
results = scenario.run(repeat=5)

# All agents passed
expect(results).all_pass()

# Success rate
expect(results).success_rate_above(0.95)
expect(results).success_rate_equals(1.0)

# Average metrics
expect(results).avg_cost_under(0.10)
expect(results).avg_latency_under(2000)

# Agent-specific
expect(results).agent_success_rate("agent_1", above=0.90)
```

## Soft Failures

Collect all failures instead of stopping at the first one.

```python
from attest import soft_fail

with soft_fail():
    expect(result).output_contains("hello")  # Failure recorded
    expect(result).cost_under(0.01)          # Failure recorded
    expect(result).passes_judge("...")       # Still executes
    # After context, all 3 failures reported
```

## Error Messages

When an assertion fails, you get detailed error information:

```
✗ Assertion failed: output_contains("goodbye")
  Expected output to contain: goodbye
  Actual output: hello world
  Suggestion: Check if the prompt was clear
```

## Auto-Documentation

The `expect()` function provides full introspection:

```python
from attest import expect

# List all available assertions
result = agent.run("...")
chain = expect(result)

print(dir(chain))  # All assertion methods
help(chain.output_contains)  # Method documentation
```

## API Reference

::: attest.expect.ExpectChain
    options:
      show_source: true
      show_root_heading: true

## Related

- [Python SDK Overview](index.md) — Core concepts and patterns
- [Adapters Reference](adapters.md) — Provider integrations
