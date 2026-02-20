# Migrate from Manual Testing

Automate agent QA with Attest's assertion framework.

## Why Automate?

Manual QA is slow and error-prone. Attest lets you encode QA checklists as executable tests:

- **Repeatability** — Run the same checks instantly
- **Regression detection** — Catch changes after model updates
- **CI/CD integration** — Fail builds if quality drops
- **Historical tracking** — See quality trends over time
- **Cost visibility** — Monitor token usage and latency

## Common Manual Checks → Attest Assertions

### Check 1: Output Contains Specific Text

=== "Manual QA Checklist"

    ```
    [ ] Response mentions the answer
    [ ] Response doesn't mention errors
    [ ] Response is not empty
    ```

=== "Attest Assertions"

    ```python
    from attest import expect

    result = agent.run("question")

    (expect(result)
      .output_contains("answer")
      .output_not_contains("error")
      .output_not_contains(""))
    ```

### Check 2: Response Structure

=== "Manual QA Checklist"

    ```
    [ ] Response is valid JSON
    [ ] Contains required fields (name, email)
    [ ] No null values in important fields
    ```

=== "Attest Assertions"

    ```python
    from attest import expect
    import json

    result = agent.run("Generate user")

    (expect(result)
      .matches_schema({
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "email": {"type": "string"}
        },
        "required": ["name", "email"]
      }))
    ```

### Check 3: Performance Metrics

=== "Manual QA Checklist"

    ```
    [ ] Response completes in under 5 seconds
    [ ] API costs less than $0.10
    [ ] Uses correct model/provider
    ```

=== "Attest Assertions"

    ```python
    from attest import expect

    result = agent.run("question")

    (expect(result)
      .latency_under(5000)
      .cost_under(0.10)
      .trace_contains_model("gpt-4o-mini"))
    ```

### Check 4: Semantic Correctness

=== "Manual QA Checklist"

    ```
    [ ] Answer is relevant to question
    [ ] Answer is grammatically correct
    [ ] Answer is coherent and well-written
    ```

=== "Attest Assertions"

    ```python
    from attest import expect

    result = agent.run("question")

    (expect(result)
      .semantically_similar_to("expected answer", threshold=0.85)
      .passes_judge("Is this grammatically correct?")
      .passes_judge("Is this coherent and helpful?"))
    ```

## Checklist to Test Suite Conversion

### Step 1: List All Manual Checks

Document what you currently check:

```
Response Quality:
  [ ] Contains answer to question
  [ ] No error messages
  [ ] Proper capitalization

Performance:
  [ ] Completes in <5s
  [ ] Costs <$0.10
  [ ] Uses GPT-4o-mini

Semantic:
  [ ] Answers the actual question
  [ ] Grammatically correct
  [ ] Professional tone
```

### Step 2: Map to Assertions

Convert each check to an assertion:

```python
from attest import expect

result = agent.run("What is 2+2?")

# Response Quality
(expect(result)
  .output_contains("4")
  .output_not_contains("error")
  .output_starts_with(result.output[0].upper()))

# Performance
expect(result).latency_under(5000)
expect(result).cost_under(0.10)
expect(result).trace_contains_model("gpt-4o-mini")

# Semantic
(expect(result)
  .semantically_similar_to("The answer is 4")
  .passes_judge("Is this grammatically correct?")
  .passes_judge("Does this sound professional?"))
```

### Step 3: Organize into Test Cases

Group assertions by scenario:

```python
def test_math_question():
    """Agent should answer math questions."""
    result = agent.run("What is 2+2?")

    (expect(result)
      .output_contains("4")
      .output_not_contains("error")
      .latency_under(5000)
      .cost_under(0.10)
      .passes_judge("Is the answer correct?"))

def test_history_question():
    """Agent should answer history questions."""
    result = agent.run("When was the Declaration of Independence?")

    (expect(result)
      .output_contains("1776")
      .passes_judge("Is this historically accurate?")
      .semantically_similar_to("July 4, 1776"))
```

### Step 4: Run Tests Locally

```bash
python -m pytest test_agent.py -v
```

### Step 5: Integrate into CI/CD

Add to GitHub Actions:

```yaml
name: Agent QA

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v4
      - run: pip install attest-ai pytest
      - run: pytest test_agent.py
```

## Complete Example

### Manual QA Workflow (Before)

```
1. Run agent manually in notebook
2. Read output line by line
3. Check:
   - Does it answer the question?
   - Any errors or warnings?
   - Is the output grammatical?
   - How long did it take?
   - How much did it cost?
4. Document findings in Google Sheet
5. Notify team if anything broke
```

### Automated Test Suite (After)

```python
from attest import expect
import pytest

@pytest.fixture
def agent():
    from app import create_agent
    return create_agent()

def test_factual_questions(agent):
    """Test agent on factual questions."""
    test_cases = [
        ("What is the capital of France?", "Paris"),
        ("When was WWII?", "1939-1945"),
        ("Who is the President of the US?", "Biden")
    ]

    for question, expected in test_cases:
        result = agent.run(question)

        (expect(result)
          .output_contains(expected)
          .output_not_contains("error")
          .latency_under(5000)
          .cost_under(0.10)
          .passes_judge("Is this factually correct?"))

def test_response_quality(agent):
    """Test response quality metrics."""
    result = agent.run("What is 2+2?")

    (expect(result)
      .output_not_contains("")
      .passes_judge("Is this grammatically correct?")
      .passes_judge("Is this professionally written?"))

def test_performance(agent):
    """Test performance constraints."""
    result = agent.run("complex question")

    (expect(result)
      .latency_under(5000)
      .cost_under(0.25))
```

Run it:

```bash
pytest test_agent.py -v
```

Output:

```
test_agent.py::test_factual_questions[Paris] PASSED
test_agent.py::test_factual_questions[1939-1945] PASSED
test_agent.py::test_factual_questions[Biden] PASSED
test_agent.py::test_response_quality PASSED
test_agent.py::test_performance PASSED

======================== 5 passed in 12.34s ========================
```

## Benefits

### 1. Consistency

Same checks every run, no human error:

```python
# This always passes or fails consistently
expect(result).output_contains("hello")
```

### 2. Speed

Run full QA in seconds instead of hours:

```bash
# Runs 100 test cases in <60s
pytest test_agent.py --tb=short
```

### 3. Regression Detection

Fail immediately if quality drops:

```python
# Catches unexpected changes
expect(result).cost_under(0.10)  # Fails if cost increased
expect(result).latency_under(5000)  # Fails if agent is slow
```

### 4. Documentation

Tests serve as specification:

```python
def test_agent_responds_to_math():
    """Agent should correctly answer math questions."""
    result = agent.run("What is 2+2?")
    expect(result).output_contains("4")
```

## FAQ

**How do I migrate an existing QA spreadsheet?**

1. Export checklist items from your spreadsheet
2. Map each item to an assertion
3. Organize into test functions
4. Run `pytest` to validate

**What about edge cases I haven't tested?**

Add them as you find them:

```python
def test_edge_case_empty_input(agent):
    """Agent should handle empty input gracefully."""
    result = agent.run("")
    expect(result).output_not_contains("error")
```

**Can I test in the cloud?**

Yes, run pytest in CI/CD. Attest works everywhere Python runs.

**How do I monitor quality over time?**

Check CI logs to see pass rates by date. For dashboards, integrate with your monitoring platform.

## Related

- [Expect DSL Reference](../reference/python/expect.md) — All assertion methods
- [Getting Started](../getting-started.md) — Quick start guide
- [Migration from PromptFoo](from-promptfoo.md) — Migrate from another framework
