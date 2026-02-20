# Writing a Framework Adapter

Learn how to write a custom Attest adapter to instrument your AI framework.

## When to Write an Adapter

Write an adapter when you want Attest to automatically capture traces from a framework. Common reasons:

- **Framework not supported** — LangChain, CrewAI, and LlamaIndex work out of the box, but your framework doesn't
- **Custom agents** — You built your own agent framework and want trace instrumentation
- **Custom tools** — You have framework-specific tool calling patterns
- **Cost tracking** — You want to track token costs for a custom provider

You don't need an adapter to use Attest! You can always manually create result objects and use expect() directly.

## Adapter Architecture

An adapter is a Python class that:

1. **Intercepts calls** to your framework
2. **Captures trace data** (model calls, tool uses, latency, cost)
3. **Builds result objects** for Attest to validate
4. **Returns structured output** for assertions

## BaseAdapter Reference

All adapters inherit from `BaseAdapter`:

```python
from attest.adapters import BaseAdapter

class MyAdapter(BaseAdapter):
    def capture_trace(self, fn, args, kwargs):
        """Intercept function call and capture trace."""
        # 1. Call the original function
        result = fn(*args, **kwargs)

        # 2. Extract trace data
        trace = extract_trace(result)
        cost = calculate_cost(result)
        latency = result.duration

        # 3. Return structured result
        return {
            'output': extract_output(result),
            'cost': cost,
            'latency_ms': latency,
            'trace': trace,
            'metadata': {}
        }

    def setup(self):
        """Called on adapter initialization."""
        pass

    def teardown(self):
        """Called on adapter cleanup."""
        pass
```

## Step-by-Step Implementation

### Step 1: Understand Your Framework

First, understand how your framework works. Example with a hypothetical `MyAgent`:

```python
class MyAgent:
    def run(self, prompt: str) -> AgentResult:
        # Calls models
        response = self.llm.complete(prompt)

        # Calls tools
        if response.tool_call:
            tool_result = self.tools[response.tool_call.name](...)
            response = self.llm.complete(tool_result)

        return AgentResult(
            output=response.text,
            duration_ms=time_taken,
            model_calls=[...],
            tool_calls=[...],
            tokens_in=100,
            tokens_out=50
        )
```

### Step 2: Create the Adapter Class

```python
from attest.adapters import BaseAdapter
from typing import Any

class MyAgentAdapter(BaseAdapter):
    """Adapter for MyAgent framework."""

    def __init__(self):
        super().__init__()
        self.name = "my_agent"

    def setup(self):
        """Register hooks when adapter is loaded."""
        # Could patch MyAgent.run here if needed
        pass

    def capture_trace(self, fn, args, kwargs):
        """Capture trace from MyAgent.run()."""
        # Call the original method
        start_time = time.time()
        result = fn(*args, **kwargs)
        latency_ms = (time.time() - start_time) * 1000

        # Build trace structure
        trace = self._build_trace(result)

        # Calculate cost (tokens * rate)
        cost = self._calculate_cost(result)

        # Return Attest-compatible result
        return {
            'output': result.output,
            'cost': cost,
            'latency_ms': latency_ms,
            'trace': trace,
            'metadata': {
                'model_calls': len(result.model_calls),
                'tool_calls': len(result.tool_calls)
            }
        }

    def _build_trace(self, result: Any) -> dict:
        """Build trace tree from framework result."""
        return {
            'model_calls': [
                {
                    'model': call.model,
                    'input_tokens': call.tokens_in,
                    'output_tokens': call.tokens_out,
                    'cost': call.cost
                }
                for call in result.model_calls
            ],
            'tool_calls': [
                {
                    'name': call.name,
                    'args': call.args,
                    'result': call.result
                }
                for call in result.tool_calls
            ]
        }

    def _calculate_cost(self, result: Any) -> float:
        """Calculate token cost from result."""
        total_cost = 0.0
        for call in result.model_calls:
            # Assume result has cost data
            total_cost += call.cost
        return total_cost
```

### Step 3: Register the Adapter

Make the adapter discoverable:

```python
# attest/adapters/my_agent.py

from attest.adapters import BaseAdapter
from .my_agent_adapter import MyAgentAdapter

# Export for public use
__all__ = ['MyAgentAdapter', 'create_agent']

def create_agent(*args, **kwargs):
    """Create a MyAgent instance with Attest instrumentation."""
    adapter = MyAgentAdapter()
    return adapter.instrument(MyAgent(*args, **kwargs))
```

Then users import it like:

```python
from attest.adapters import my_agent

agent = my_agent.create_agent(config)
result = agent.run("question")

expect(result).output_contains("...")
```

### Step 4: Handle Edge Cases

Add error handling for common issues:

```python
def capture_trace(self, fn, args, kwargs):
    """Capture trace with error handling."""
    try:
        result = fn(*args, **kwargs)
    except Exception as e:
        # Return failed result
        return {
            'output': f"Error: {str(e)}",
            'cost': 0.0,
            'latency_ms': 0,
            'trace': {'error': str(e)},
            'metadata': {'error': True}
        }

    # Process normally
    return self._process_result(result)
```

### Step 5: Write Tests

Test your adapter:

```python
import pytest
from attest import expect
from my_agent_adapter import MyAgentAdapter

@pytest.fixture
def adapter():
    return MyAgentAdapter()

def test_adapter_captures_output(adapter):
    """Adapter should capture output."""
    mock_result = MockAgentResult(output="Hello, world!")
    result = adapter.capture_trace(lambda: mock_result, [], {})

    assert result['output'] == "Hello, world!"

def test_adapter_calculates_cost(adapter):
    """Adapter should calculate token cost."""
    mock_result = MockAgentResult(
        model_calls=[MockCall(cost=0.05), MockCall(cost=0.03)]
    )
    result = adapter.capture_trace(lambda: mock_result, [], {})

    assert result['cost'] == 0.08

def test_expect_integration(adapter):
    """Adapter should work with expect()."""
    mock_result = MockAgentResult(output="Expected output")
    result = adapter.capture_trace(lambda: mock_result, [], {})

    # Should work with expect
    expect(result).output_contains("Expected")
```

## Real-World Example: LangChain Adapter

Here's how the built-in LangChain adapter works (simplified):

```python
from attest.adapters import BaseAdapter
from langchain.callbacks import BaseCallbackHandler

class LangChainAdapter(BaseAdapter):
    """Instrument LangChain agents and chains."""

    def __init__(self):
        super().__init__()
        self.name = "langchain"
        self.current_trace = None

    def capture_trace(self, fn, args, kwargs):
        """Capture LangChain execution."""
        # Create callback handler to track execution
        handler = TraceCallbackHandler()
        kwargs['callbacks'] = [handler]

        # Run the chain/agent
        start_time = time.time()
        result = fn(*args, **kwargs)
        latency_ms = (time.time() - start_time) * 1000

        # Extract output
        if isinstance(result, dict):
            output = result.get('output', str(result))
        else:
            output = str(result)

        return {
            'output': output,
            'cost': handler.total_cost,
            'latency_ms': latency_ms,
            'trace': {
                'model_calls': handler.model_calls,
                'tool_calls': handler.tool_calls,
                'steps': handler.steps
            },
            'metadata': {
                'framework': 'langchain',
                'agent_type': type(fn).__name__
            }
        }

class TraceCallbackHandler(BaseCallbackHandler):
    """Callback to capture LangChain execution trace."""

    def __init__(self):
        self.model_calls = []
        self.tool_calls = []
        self.steps = []
        self.total_cost = 0.0

    def on_llm_start(self, serialized, prompts, **kwargs):
        """Called when LLM is about to run."""
        self.steps.append(('llm_start', serialized.get('name')))

    def on_llm_end(self, response, **kwargs):
        """Called when LLM completes."""
        # Track cost from response
        if hasattr(response, 'llm_output'):
            cost = self._calculate_cost(response)
            self.total_cost += cost
            self.model_calls.append({
                'model': response.llm_output.get('model'),
                'cost': cost
            })

    def on_tool_start(self, serialized, input_str, **kwargs):
        """Called when tool is executed."""
        self.tool_calls.append(serialized.get('name'))
        self.steps.append(('tool_start', serialized.get('name')))

    def _calculate_cost(self, response):
        """Calculate cost from LLM response."""
        # Implementation depends on provider
        return 0.0  # Simplified
```

## Testing Your Adapter

### Unit Tests

Test adapter components in isolation:

```python
def test_cost_calculation():
    """Adapter should calculate costs correctly."""
    adapter = MyAdapter()
    mock_result = create_mock_result(tokens_in=100, tokens_out=50)
    result = adapter.capture_trace(lambda: mock_result, [], {})

    expected_cost = (100 * 0.0005 + 50 * 0.0015) / 1000
    assert abs(result['cost'] - expected_cost) < 0.00001
```

### Integration Tests

Test with real agents:

```python
def test_adapter_with_real_agent():
    """Adapter should work with real agent."""
    agent = MyAgent()
    adapter = MyAdapter()

    result = adapter.capture_trace(agent.run, ["What is 2+2?"], {})

    assert result['output'] == "4"
    assert result['cost'] >= 0
    assert result['latency_ms'] > 0
```

### Attest Tests

Test with Attest's expect():

```python
def test_adapter_result_works_with_expect():
    """Adapter result should work with expect()."""
    agent = MyAgent()
    result = agent.run("question")

    expect(result).output_contains("answer").cost_under(0.10)
```

## Advanced: Custom Trace Tree

Build more complex trace structures:

```python
def _build_trace_tree(self, result):
    """Build detailed trace tree."""
    return {
        'root': {
            'name': 'agent',
            'model': result.model,
            'children': [
                {
                    'name': 'tool_call',
                    'tool': call.name,
                    'children': [
                        {
                            'name': 'llm_call',
                            'model': call.follow_up_model,
                            'tokens_in': call.tokens_in,
                            'tokens_out': call.tokens_out
                        }
                    ]
                }
                for call in result.tool_calls
            ]
        }
    }
```

Then use it in assertions:

```python
result = agent.run("question")

expect(result).trace_tree_valid()  # Validates structure
```

## Troubleshooting

**"Adapter not capturing cost"**

Check that your framework exposes cost data. If not, calculate from token counts:

```python
cost = (tokens_in * input_rate + tokens_out * output_rate) / 1000
```

**"Latency seems wrong"**

Ensure you're timing the right part. Include framework overhead:

```python
start = time.time()
result = fn(*args, **kwargs)
latency = (time.time() - start) * 1000
```

**"Trace is incomplete"**

Use callbacks or hooks to capture all events, not just the final result:

```python
handler = TraceHandler()
kwargs['callbacks'] = [handler]  # Ensure callbacks are passed
result = fn(*args, **kwargs)
```

## Related

- [Adapters Reference](../reference/python/adapters.md) — All built-in adapters
- [Python SDK Overview](../reference/python/index.md) — Core concepts
- [Expect DSL Reference](../reference/python/expect.md) — Assertion methods
