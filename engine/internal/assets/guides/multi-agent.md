# Multi-Agent Testing Guide

Attest provides first-class support for testing multi-agent systems through hierarchical trace trees, cross-agent assertions, and delegation tracking. All multi-agent assertions are Layer 7 — deterministic, free, and instant.

## Trace Tree Model

A trace tree represents the execution hierarchy of a multi-agent system. Each agent produces a `Trace`. When an agent delegates to a sub-agent, the sub-agent's trace is nested under the parent via an `agent_call` step with a `sub_trace` field.

```text
orchestrator (root trace)
├── llm_call: plan
├── tool_call: fetch_context
└── agent_call: researcher (sub_trace)
    ├── tool_call: search_web
    └── agent_call: writer (sub_trace)
        └── tool_call: write_doc
```

Each node in the tree is a `Trace` with its own `agent_id`, `steps`, `output`, and `metadata`. The tree is constructed automatically when using `delegate()`.

## `delegate()` Context Manager

The `delegate()` context manager creates a child `TraceBuilder` linked to the current parent. On exit, it adds an `agent_call` step to the parent with the child's built trace.

```python
import attest
from attest.delegate import delegate
from attest.trace import TraceBuilder
from attest.simulation._context import _active_builder

# Inside an agent's run context:
parent = TraceBuilder(agent_id="orchestrator")
parent.set_input(task="Process refund")

# Set parent as the active builder
token = _active_builder.set(parent)

with delegate("researcher") as child:
    child.add_tool_call("search_web", args={"q": "refund policy"})
    child.set_output(message="Policy found: 30-day window.")

    with delegate("writer") as grandchild:
        grandchild.add_tool_call("write_doc", args={"title": "Refund Report"})
        grandchild.set_output(message="Report drafted.")

parent.set_output(message="Refund processed.")
_active_builder.reset(token)

trace = parent.build()
```

### How It Works

1. `delegate(agent_id)` reads the active `TraceBuilder` from `_active_builder` context variable.
2. Creates a child `TraceBuilder` with the given `agent_id` and sets `parent_trace_id` to the parent's trace ID.
3. Sets the child as the new active builder (so nested `delegate()` calls chain correctly).
4. On exit, resets the active builder to the parent and appends an `agent_call` step with the child's built trace.

### Error Handling

`delegate()` raises `RuntimeError` if called outside an active `TraceBuilder` context:

```python
# This raises RuntimeError:
# "delegate() must be used within an Agent.run() context. No active TraceBuilder found."
with delegate("sub-agent") as child:
    pass
```

## TraceTree API

Build a `TraceTree` from an `AgentResult` or directly from a `Trace`:

```python
from attest.trace_tree import TraceTree

# From AgentResult
tree = result.trace_tree()

# From Trace directly
tree = TraceTree(root=trace)
```

### Properties and Methods

#### `agents -> list[str]`

All `agent_id` values in the tree, depth-first order:

```python
tree.agents  # ["orchestrator", "researcher", "writer"]
```

#### `depth -> int`

Maximum nesting depth. Root with no sub-agents = 0:

```python
tree.depth  # 2 (orchestrator -> researcher -> writer)
```

#### `delegations -> list[tuple[str, str]]`

All (parent_agent_id, child_agent_id) delegation pairs:

```python
tree.delegations
# [("orchestrator", "researcher"), ("researcher", "writer")]
```

#### `find_agent(agent_id) -> Trace | None`

Find a sub-trace by agent_id:

```python
researcher = tree.find_agent("researcher")
researcher.output  # {"message": "Policy found: 30-day window."}
```

#### `flatten() -> list[Trace]`

All traces in depth-first order:

```python
all_traces = tree.flatten()
# [orchestrator_trace, researcher_trace, writer_trace]
```

#### `all_tool_calls() -> list[Step]`

All `tool_call` steps across the entire tree:

```python
tools = tree.all_tool_calls()
[t.name for t in tools]  # ["fetch_context", "search_web", "write_doc"]
```

#### Aggregate Metrics

Sum metrics across all agents in the tree:

```python
tree.aggregate_tokens   # Total tokens across all agents
tree.aggregate_cost     # Total cost_usd across all agents
tree.aggregate_latency  # Total latency_ms across all agents
```

## Cross-Agent Assertions

All multi-agent assertions use `TYPE_TRACE_TREE` (Layer 7) and are evaluated by the engine's `TraceTreeEvaluator`.

### `agent_called(agent_id)`

Assert a specific agent was invoked somewhere in the trace tree:

```python
expect(result).agent_called("researcher")
expect(result).agent_called("writer")
```

### `delegation_depth(max_depth)`

Assert the trace tree does not exceed a maximum nesting depth:

```python
expect(result).delegation_depth(3)  # Max 3 levels deep
```

### `agent_output_contains(agent_id, value)`

Assert a sub-agent's output contains a specific string:

```python
expect(result).agent_output_contains("researcher", "policy found")
expect(result).agent_output_contains("writer", "report", case_sensitive=False)
```

### `cross_agent_data_flow(from_agent, to_agent, field)`

Assert that a field from one agent's output appears in another agent's input:

```python
expect(result).cross_agent_data_flow(
    from_agent="researcher",
    to_agent="writer",
    field="findings",
)
```

The engine extracts `field` from `from_agent`'s output JSON, serializes it, and checks that the serialized value appears as a substring of `to_agent`'s input.

### `follows_transitions(transitions)`

Assert that all agent delegations in the trace tree match the allowed transition pairs:

```python
expect(result).follows_transitions([
    ("orchestrator", "researcher"),
    ("orchestrator", "writer"),
    ("researcher", "writer"),
])
```

The engine walks the trace tree depth-first, collects every (parent_agent_id, child_agent_id) delegation pair, and verifies each pair exists in the allowed list. Any delegation not in the list causes a failure.

This is useful for enforcing choreography — ensuring agents only delegate to approved sub-agents.

### `aggregate_cost_under(max_cost)`

Assert the total cost across all agents is under a threshold:

```python
expect(result).aggregate_cost_under(0.10)
```

### `aggregate_tokens_under(max_tokens)`

Assert the total token usage across all agents is under a threshold:

```python
expect(result).aggregate_tokens_under(5000)
```

### Soft Failures

All multi-agent assertions support the `soft` parameter. Soft failures produce `soft_fail` status instead of `hard_fail`, allowing CI to warn without blocking:

```python
expect(result).delegation_depth(2, soft=True)
expect(result).aggregate_cost_under(0.05, soft=True)
```

## Choreography Validation

Choreography validation ensures agents interact according to a defined protocol. Combine `follows_transitions` with other assertions to fully validate multi-agent workflows:

```python
from attest import expect

def test_research_pipeline(result):
    tree = result.trace_tree()

    # Structural validation
    expect(result).delegation_depth(3)
    expect(result).agent_called("orchestrator")
    expect(result).agent_called("researcher")
    expect(result).agent_called("writer")

    # Choreography — only allowed delegation paths
    expect(result).follows_transitions([
        ("orchestrator", "researcher"),
        ("orchestrator", "writer"),
        ("researcher", "writer"),
    ])

    # Data flow — researcher findings reach writer
    expect(result).cross_agent_data_flow(
        from_agent="researcher",
        to_agent="writer",
        field="findings",
    )

    # Output quality
    expect(result).agent_output_contains("writer", "report")

    # Cost governance
    expect(result).aggregate_cost_under(0.10)
    expect(result).aggregate_tokens_under(5000)
```

## Complete Example

End-to-end multi-agent test with simulation, delegation, and assertions:

```python
import attest
from attest.delegate import delegate
from attest.trace import TraceBuilder
from attest.result import AgentResult
from attest.simulation import scenario, repeat, FRIENDLY_USER, MockToolRegistry
from attest.simulation._context import _active_builder
from attest import expect


def run_research_pipeline(query: str) -> AgentResult:
    """Simulate a 3-agent research pipeline."""
    parent = TraceBuilder(agent_id="orchestrator")
    parent.set_input(query=query)
    token = _active_builder.set(parent)

    try:
        parent.add_llm_call("plan", result={"plan": "research then write"})

        with delegate("researcher") as researcher:
            researcher.add_tool_call(
                "search_web",
                args={"q": query},
                result={"findings": "Test frameworks improve reliability."},
            )
            researcher.set_output(
                message="Research complete.",
                findings="Test frameworks improve reliability.",
            )

        with delegate("writer") as writer:
            writer.add_tool_call(
                "write_doc",
                args={"title": "Report", "content": "Test frameworks improve reliability."},
            )
            writer.set_output(message="Report drafted successfully.")

        parent.set_output(message="Pipeline complete. Report ready.")
        parent.set_metadata(total_tokens=1500, cost_usd=0.015, latency_ms=3000)
    finally:
        _active_builder.reset(token)

    return AgentResult(trace=parent.build())


@repeat(n=5)
@scenario(persona=FRIENDLY_USER, seed=42)
def test_research_pipeline(persona):
    result = run_research_pipeline("AI testing frameworks")

    # Layer 7: Multi-agent assertions
    expect(result).agent_called("researcher")
    expect(result).agent_called("writer")
    expect(result).delegation_depth(2)
    expect(result).follows_transitions([
        ("orchestrator", "researcher"),
        ("orchestrator", "writer"),
    ])
    expect(result).agent_output_contains("writer", "report")
    expect(result).aggregate_cost_under(0.10)

    return result


# Run the test
repeat_result = test_research_pipeline()
assert repeat_result.all_passed

# Inspect the trace tree from any individual run
tree = repeat_result.results[0].trace_tree()
print(f"Agents: {tree.agents}")
print(f"Delegations: {tree.delegations}")
print(f"Tool calls: {[t.name for t in tree.all_tool_calls()]}")
print(f"Depth: {tree.depth}")
print(f"Total cost: ${tree.aggregate_cost:.4f}")
```

Expected output:

```
Agents: ['orchestrator', 'researcher', 'writer']
Delegations: [('orchestrator', 'researcher'), ('orchestrator', 'writer')]
Tool calls: ['search_web', 'write_doc']
Depth: 1
Total cost: $0.0150
```

## Temporal Assertions

Temporal assertions verify the execution order and timing of agents within a trace tree. They are Layer 7 — deterministic, free, and instant. Each assertion requires that `Step` objects carry `started_at` and `ended_at` timestamps (populated automatically by framework adapters and `TraceBuilder` methods that accept timing arguments).

### `agent_ordered_before(a, b)`

Assert that agent `a` completed before agent `b` started. Use this to enforce sequential execution guarantees.

```python
from attest import expect

# researcher must complete before writer begins
expect(result).agent_ordered_before("researcher", "writer")
```

Fails if:

- Either agent is not present in the trace tree.
- `researcher.ended_at > writer.started_at` (they overlapped or writer ran first).

### `agents_overlap(a, b)`

Assert that agents `a` and `b` executed concurrently — their time ranges intersect. Use this to verify parallel execution in fan-out pipelines.

```python
# data-fetcher and context-loader must run in parallel
expect(result).agents_overlap("data-fetcher", "context-loader")
```

Fails if:

- Either agent is not present in the trace tree.
- The agents ran sequentially with no overlap.

### `agent_wall_time_under(agent_id, max_ms)`

Assert that a specific agent's wall-clock duration is under `max_ms` milliseconds. Use this to enforce per-agent performance budgets.

```python
# researcher must complete within 3 seconds
expect(result).agent_wall_time_under("researcher", max_ms=3000)

# writer must complete within 2 seconds (soft — warn, don't block CI)
expect(result).agent_wall_time_under("writer", max_ms=2000, soft=True)
```

Wall time is `ended_at - started_at` for the agent's root trace.

### `ordered_agents(groups)`

Assert that agents executed in a defined pipeline order. `groups` is a list of lists; agents within the same group ran concurrently, and each group completed before the next group started.

```python
# Sequential pipeline: orchestrator → [researcher, context-loader] → writer
expect(result).ordered_agents([
    ["orchestrator"],
    ["researcher", "context-loader"],  # these two run in parallel
    ["writer"],
])
```

This combines ordering and overlap assertions in a single call:

- Agents within a group must all overlap each other (or be the only member).
- The last agent in group `n` must finish before the first agent in group `n+1` starts.

### Temporal Assertion Example

End-to-end test combining structural, choreography, and temporal assertions:

```python
from attest import expect
from attest.simulation import scenario, repeat, FRIENDLY_USER


@repeat(n=3)
@scenario(persona=FRIENDLY_USER, seed=42)
def test_pipeline_timing(persona):
    result = run_research_pipeline("AI testing frameworks")

    # Structural
    expect(result).agent_called("researcher")
    expect(result).agent_called("context-loader")
    expect(result).agent_called("writer")
    expect(result).delegation_depth(2)

    # Choreography
    expect(result).follows_transitions([
        ("orchestrator", "researcher"),
        ("orchestrator", "context-loader"),
        ("orchestrator", "writer"),
    ])

    # Temporal — execution order
    expect(result).agent_ordered_before("orchestrator", "researcher")
    expect(result).agent_ordered_before("orchestrator", "context-loader")
    expect(result).agent_ordered_before("researcher", "writer")
    expect(result).agent_ordered_before("context-loader", "writer")

    # Temporal — parallel execution
    expect(result).agents_overlap("researcher", "context-loader")

    # Temporal — performance budgets
    expect(result).agent_wall_time_under("researcher", max_ms=5000)
    expect(result).agent_wall_time_under("writer", max_ms=3000)

    # Temporal — pipeline ordering (concise form)
    expect(result).ordered_agents([
        ["orchestrator"],
        ["researcher", "context-loader"],
        ["writer"],
    ])

    # Cost governance
    expect(result).aggregate_cost_under(0.10)

    return result


repeat_result = test_pipeline_timing()
assert repeat_result.all_passed
```
