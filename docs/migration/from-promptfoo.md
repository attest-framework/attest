# Migrate from PromptFoo

Upgrade from PromptFoo's YAML-based testing to Attest's fluent Python/TypeScript API.

## Why Migrate?

PromptFoo is great for prompt evaluation. Attest provides:

- **Agent testing** — Not just prompts, entire agents
- **Code-first** — Python or TypeScript instead of YAML
- **8-layer assertions** — From schema to simulation
- **Framework adapters** — LangChain, CrewAI, LlamaIndex
- **Trace inspection** — See model calls, tools, costs
- **Multi-agent** — Simulation runtime for scenarios

## Concept Mapping

| PromptFoo | Attest | Notes |
|-----------|--------|-------|
| `prompts/` | Agent functions | Code instead of files |
| `test.yaml` config | Python test functions | More expressive |
| `providers` | Adapters | Built-in providers |
| `asserts` | Assertion methods | Fluent DSL |
| `eval` | Pytest + expect() | Native test framework |
| Metric | Judge prompt | LLM evaluation |

## Step-by-Step Migration

### 1. Replace YAML Config with Python Tests

=== "PromptFoo (YAML)"

    ```yaml
    # promptfooconfig.yaml
    providers:
      - id: openai:gpt-4o-mini
        config:
          temperature: 0.7

    prompts:
      - id: simple_qa
        raw: "Answer this question: {{question}}"

    tests:
      - vars:
          question: "What is 2+2?"
        assert:
          - type: contains
            value: "4"
          - type: regex
            value: "^\\d+$"
          - type: cost
            threshold: 0.01
    ```

=== "Attest (Python)"

    ```python
    from attest import expect
    from openai import OpenAI

    client = OpenAI(api_key="sk-...", model="gpt-4o-mini")

    def test_simple_qa():
        """Test simple Q&A."""
        result = client.chat.completions.create(
            messages=[{"role": "user", "content": "Answer: What is 2+2?"}]
        )

        (expect(result)
          .output_contains("4")
          .output_matches(r"^\d+$")
          .cost_under(0.01))
    ```

### 2. Convert PromptFoo Providers to Adapters

=== "PromptFoo"

    ```yaml
    providers:
      - id: openai:gpt-4o-mini
      - id: anthropic:claude-3-sonnet
      - id: localai:mistral
    ```

=== "Attest"

    ```python
    from attest.adapters import openai, anthropic, ollama

    # OpenAI
    openai_result = openai.create_completion(
        model="gpt-4o-mini",
        messages=[...]
    )

    # Anthropic
    anthropic_result = anthropic.create_message(
        model="claude-3-sonnet",
        messages=[...]
    )

    # Local (Ollama)
    local_result = ollama.generate(
        model="mistral",
        prompt="..."
    )
    ```

### 3. Convert PromptFoo Asserts to Expect

=== "PromptFoo Asserts"

    ```yaml
    assert:
      - type: contains
        value: "success"
      - type: regex
        value: '^\d{4}-\d{2}-\d{2}$'
      - type: length
        value: 100
        threshold: 0.1
      - type: cost
        threshold: 0.05
      - type: json-path
        value: "$.status"
    ```

=== "Attest Expects"

    ```python
    (expect(result)
      .output_contains("success")
      .output_matches(r'^\d{4}-\d{2}-\d{2}$')
      .word_count_between(90, 110)
      .cost_under(0.05)
      .matches_schema({"type": "object", "properties": {"status": {}}}))
    ```

### 4. Convert Test Variables to Test Functions

=== "PromptFoo"

    ```yaml
    tests:
      - description: "Math question"
        vars:
          question: "What is 2+2?"
          topic: "math"
        assert:
          - type: contains
            value: "4"
      - description: "History question"
        vars:
          question: "When was WWII?"
          topic: "history"
        assert:
          - type: contains
            value: "1939"
    ```

=== "Attest"

    ```python
    import pytest

    @pytest.mark.parametrize("question,expected", [
        ("What is 2+2?", "4"),
        ("When was WWII?", "1939")
    ])
    def test_qa(question, expected):
        """Test Q&A across topics."""
        result = agent.run(question)
        expect(result).output_contains(expected)
    ```

### 5. Replace Metric Scripts with Judge Prompts

=== "PromptFoo"

    ```yaml
    tests:
      - vars:
          query: "Is this helpful?"
        assert:
          - type: script
            value: "result.output.length > 100"
          - type: script
            value: |
              const score = result.output.includes('yes') ? 1 : 0;
              return { pass: score > 0.5 };
    ```

=== "Attest"

    ```python
    result = agent.run("question")

    (expect(result)
      .word_count_between(100, 1000)
      .passes_judge(
        prompt="Is this response helpful?",
        model="gpt-4o",
        scoring="binary"
      ))
    ```

## Complete Migration Example

### Before: PromptFoo Setup

```yaml
# promptfooconfig.yaml
providers:
  - id: openai:gpt-4o-mini

prompts:
  - id: qa_agent
    raw: |
      You are a helpful assistant.
      Answer this question: {{query}}

tests:
  - description: "Math question"
    vars:
      query: "What is 2+2?"
    assert:
      - type: contains
        value: "4"
      - type: regex
        value: '^4$'
      - type: cost
        threshold: 0.01

  - description: "History question"
    vars:
      query: "What year did WWII end?"
    assert:
      - type: contains
        value: "1945"
      - type: cost
        threshold: 0.02

  - description: "Response quality"
    vars:
      query: "Tell me about Python"
    assert:
      - type: length
        threshold: 0.5
        value: 200
      - type: javascript
        value: |
          const words = result.output.split(' ').length;
          return { pass: words > 50, score: Math.min(words / 100, 1) };
```

Run with:

```bash
promptfoo eval
```

### After: Attest Setup

```python
# test_agent.py
import pytest
from attest import expect

class TestAgent:
    @pytest.fixture
    def agent(self):
        from my_app import create_agent
        return create_agent()

    def test_math_question(self, agent):
        """Math question should return correct answer."""
        result = agent.run("What is 2+2?")

        (expect(result)
          .output_contains("4")
          .output_matches("^4$")
          .cost_under(0.01))

    def test_history_question(self, agent):
        """History question should return correct year."""
        result = agent.run("What year did WWII end?")

        (expect(result)
          .output_contains("1945")
          .cost_under(0.02))

    def test_response_quality(self, agent):
        """Response should be comprehensive."""
        result = agent.run("Tell me about Python")

        (expect(result)
          .word_count_between(150, 500)
          .passes_judge("Is this well-written and informative?"))
```

Run with:

```bash
pytest test_agent.py -v
```

## PromptFoo → Attest Feature Map

| PromptFoo Feature | Attest Equivalent |
|-------------------|-------------------|
| Web UI evaluation | Python/TS code + pytest |
| YAML config | Python test functions |
| Multiple prompts | Multiple test functions |
| Provider selection | Adapter selection |
| Contains assert | `.output_contains()` |
| Regex assert | `.output_matches()` |
| Cost tracking | `.cost_under()` |
| Custom JS eval | `.passes_judge()` |
| CSV test data | pytest parametrize |
| Batch evaluation | pytest + parameterize |

## Key Differences

### 1. Code Over Config

PromptFoo uses YAML, Attest uses Python/TypeScript. More expressive and testable:

```python
# Attest: can use variables, loops, logic
for query in queries:
    result = agent.run(query)
    expect(result).output_contains("...")
```

### 2. Agents Over Prompts

Test entire agents, not just prompts:

```python
from attest.adapters import langchain

# Full LangChain agent with tools
agent = langchain.create_agent(llm, tools)
result = agent.invoke({"input": "..."})

expect(result).trace_contains_tool("google_search")
```

### 3. Fluent API Over Assertions List

Chain assertions naturally:

```python
# Attest: fluent, readable
(expect(result)
  .output_contains("answer")
  .cost_under(0.05)
  .passes_judge("Is correct?"))

# PromptFoo: list of assertion objects
assert:
  - type: contains
    value: "answer"
  - type: cost
    threshold: 0.05
```

## Migration Checklist

- [ ] Convert `promptfooconfig.yaml` to test functions
- [ ] Replace provider declarations with adapters
- [ ] Map all test cases to `@pytest.mark.parametrize`
- [ ] Convert `assert` statements to `.expect()` chains
- [ ] Replace JavaScript metrics with `.passes_judge()`
- [ ] Run `pytest` and verify all tests pass
- [ ] Add to CI/CD pipeline
- [ ] Remove PromptFoo config files

## FAQ

**Can I run PromptFoo and Attest in parallel?**

Yes, during migration. But consolidate to Attest once complete.

**How do I evaluate multiple prompts like PromptFoo?**

Use test parametrization:

```python
@pytest.mark.parametrize("prompt,expected", [
    ("Prompt A", "Expected A"),
    ("Prompt B", "Expected B")
])
def test_prompts(prompt, expected):
    result = agent.run(prompt)
    expect(result).output_contains(expected)
```

**How do I replace PromptFoo's batch mode?**

pytest automatically runs all test functions:

```bash
pytest test_agent.py  # Runs all tests
```

**What about PromptFoo's web UI?**

Attest is code-first. Use IDE + test output for feedback. No UI needed.

## Related

- [Expect DSL Reference](../reference/python/expect.md) — All assertion methods
- [Getting Started](../getting-started.md) — Quick start
- [Adapters Reference](../reference/python/adapters.md) — Provider integrations
