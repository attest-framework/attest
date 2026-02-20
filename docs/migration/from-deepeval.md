# Migrate from DeepEval

Move from DeepEval to Attest for more powerful, composable agent testing.

## Why Migrate?

DeepEval focuses on metric evaluation. Attest provides:

- **Composable assertions** — Chain validations fluently
- **8-layer stack** — From schema to simulation
- **Trace inspection** — See exactly what your agent did
- **Multi-agent testing** — Built-in simulation runtime
- **Framework adapters** — LangChain, CrewAI, LlamaIndex, more

## Concept Mapping

| DeepEval | Attest | Notes |
|----------|--------|-------|
| `Metric` | Assertion method | e.g., `output_contains()` |
| `evaluate()` | `expect()` | Entry point |
| `Faithfulness` | `passes_judge()` | LLM evaluation |
| `AnswerRelevancy` | `semantically_similar_to()` | Embedding-based |
| `RAGAS` | 8-layer stack | Comprehensive validation |
| `DeepEvalConfig` | `config` module | Global configuration |

## Step-by-Step Migration

### 1. Replace Metric with Expect

=== "DeepEval"

    ```python
    from deepeval.metrics import Faithfulness

    metric = Faithfulness()
    result = metric.measure(
        output="The sky is blue",
        context="The sky appears blue due to scattering"
    )
    print(result.score)
    ```

=== "Attest"

    ```python
    from attest import expect

    result = agent.run("What color is the sky?")

    # Use LLM judge instead
    expect(result).passes_judge("Is the answer faithful to facts?")
    ```

### 2. Replace evaluate() with expect()

=== "DeepEval"

    ```python
    from deepeval import evaluate
    from deepeval.metrics import Faithfulness, AnswerRelevancy

    metrics = [
        Faithfulness(model="gpt-4o"),
        AnswerRelevancy(model="gpt-4o")
    ]

    results = evaluate(
        test_cases=test_cases,
        metrics=metrics
    )

    print(f"Pass rate: {results.get_pass_rate()}")
    ```

=== "Attest"

    ```python
    from attest import expect

    for test_case in test_cases:
        result = agent.run(test_case.input)

        (expect(result)
          .passes_judge("Is this faithful to facts?")
          .passes_judge("Is this relevant to the question?")
          .output_not_contains("error"))
    ```

### 3. Migrate RAGAS Evaluation

DeepEval's RAGAS metrics map to Attest's 8-layer stack:

=== "DeepEval (RAGAS)"

    ```python
    from deepeval.metrics import RAGAS

    metric = RAGAS(
        model="gpt-4o",
        include_harmfulness=True,
        include_maliciousness=False
    )

    result = metric.measure(
        output="The answer",
        retrieval_context=["context"],
        expected_output="Expected answer"
    )
    ```

=== "Attest (8 Layers)"

    ```python
    from attest import expect

    result = rag_agent.run("question")

    (expect(result)
      # Layer 1: Schema
      .matches_schema({"type": "string"})
      # Layer 2: Constraints
      .cost_under(0.10)
      # Layer 3: Trace
      .trace_contains_tool("retrieval")
      # Layer 4: Content
      .output_contains("key fact")
      # Layer 5: Embedding
      .semantically_similar_to("expected answer")
      # Layer 6: Judge
      .passes_judge("Is this harmful?", scoring="binary")
      # Layer 7: Trace Tree
      .trace_tree_valid())
    ```

### 4. Replace TestCase with Direct Assertions

=== "DeepEval"

    ```python
    from deepeval.test_cases import LLMTestCase

    test_cases = [
        LLMTestCase(
            input="What is 2+2?",
            expected_output="4"
        ),
        LLMTestCase(
            input="What is 3+3?",
            expected_output="6"
        )
    ]
    ```

=== "Attest"

    ```python
    from attest import expect

    inputs = [
        {"question": "What is 2+2?", "expected": "4"},
        {"question": "What is 3+3?", "expected": "6"}
    ]

    for test in inputs:
        result = agent.run(test["question"])
        expect(result).output_contains(test["expected"])
    ```

### 5. Use Adapters Instead of Custom Metrics

=== "DeepEval"

    ```python
    from deepeval.metrics import CustomMetric

    class MyMetric(CustomMetric):
        def measure(self, output, **kwargs):
            # Custom evaluation logic
            return output.lower().count("good")
    ```

=== "Attest"

    ```python
    from attest import expect

    # Use built-in judge
    result = agent.run("...")

    expect(result).passes_judge(
        prompt="Does this contain positive sentiment?",
        model="gpt-4o",
        scoring="scale_0_10"
    )

    # Or write custom adapter for complex logic
    from attest.adapters import BaseAdapter

    class CustomEval(BaseAdapter):
        def evaluate(self, result):
            return result.output.lower().count("good")
    ```

## Complete Example

### Before: DeepEval

```python
from deepeval import evaluate
from deepeval.metrics import (
    Faithfulness,
    AnswerRelevancy,
    Correctness
)
from deepeval.test_cases import LLMTestCase

test_cases = [
    LLMTestCase(
        input="What is the capital of France?",
        expected_output="Paris"
    ),
    LLMTestCase(
        input="What is 2+2?",
        expected_output="4"
    )
]

metrics = [
    Faithfulness(model="gpt-4o"),
    AnswerRelevancy(model="gpt-4o"),
    Correctness(model="gpt-4o")
]

results = evaluate(test_cases, metrics)
print(f"Pass rate: {results.get_pass_rate()}")
```

### After: Attest

```python
from attest import expect

test_cases = [
    {"question": "What is the capital of France?", "expected": "Paris"},
    {"question": "What is 2+2?", "expected": "4"}
]

for test in test_cases:
    result = agent.run(test["question"])

    (expect(result)
      .output_contains(test["expected"])
      .passes_judge("Is the answer faithful?", model="gpt-4o")
      .passes_judge("Does it answer the question?", model="gpt-4o")
      .passes_judge("Is the answer correct?", model="gpt-4o"))

print("All tests passed!")
```

## Benefits of Attest

### 1. Composable Assertions

Stack multiple validations in one fluent chain:

```python
(expect(result)
  .output_contains("answer")
  .cost_under(0.05)
  .passes_judge("Is correct?"))
```

### 2. Trace Inspection

See exactly what your agent did:

```python
expect(result).trace_contains_tool("google_search")
expect(result).trace_depth_under(5)
```

### 3. Framework Adapters

Built-in support for LangChain, CrewAI, LlamaIndex:

```python
from attest.adapters import langchain

agent = langchain.create_agent(...)
result = agent.invoke(...)

# Attest auto-captures trace
expect(result).trace_contains_tool("...")
```

### 4. Multi-Agent Testing

Test entire scenarios with multiple agents:

```python
from attest import simulate

scenario = simulate.scenario()
scenario.add_agent(researcher)
scenario.add_agent(reviewer)
results = scenario.run(repeat=5)

expect(results).success_rate_above(0.95)
```

## FAQ

**Can I use DeepEval and Attest together?**

Yes, but we recommend migrating completely for consistency. You can gradually migrate test suites one at a time.

**What about RAGAS?**

RAGAS concepts map to Attest's 8-layer stack. Use `passes_judge()` with custom prompts instead of RAGAS metrics.

**How do I test retrieval quality?**

Use trace inspection to verify retrieval:

```python
expect(result).trace_contains_tool("retrieval")
```

**What about synthetic data generation?**

Attest focuses on assertion. For synthetic data, continue using DeepEval's tools or others.

## Related

- [Expect DSL Reference](../reference/python/expect.md) — All assertion methods
- [Adapters Reference](../reference/python/adapters.md) — Framework integrations
- [Writing a Framework Adapter](../tutorials/writing-a-framework-adapter.md) — Custom adapters
