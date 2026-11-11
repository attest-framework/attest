# Simulation Runtime Guide

Use simulation helpers to repeat an agent run, supply controlled tool results and test how it handles faults or different user personas. These helpers compose with the assertion pipeline and are beta in 1.1.0. They do not automatically stop your agent's own model or network calls.

This runtime is distinct from `ATTEST_SIMULATION=1`: that setting disables Attest's embedding and judge providers while deterministic checks still run ([verdicts](verdicts.md#simulation-mode)). Mocking a tool here affects calls recorded through `TraceBuilder.add_tool_call()`, not arbitrary external HTTP clients.

## Overview

The simulation module (`attest.simulation`) provides four decorators:

| Decorator       | Purpose                                                           |
| --------------- | ----------------------------------------------------------------- |
| `@scenario`     | Full simulation configuration: persona, mocks, faults, multi-turn |
| `@repeat`       | Run a test function N times and collect statistical results       |
| `@mock_tool`    | Tag a function as a mock tool implementation                      |
| `@fault_inject` | Add random errors and latency jitter to a function                |

And three built-in personas for simulating different user behaviors.

## `@scenario` Decorator

The `@scenario` decorator wraps an agent test function with simulation context. It accepts a `ScenarioConfig` worth of parameters and returns a `ScenarioResult`.

<!-- snippet: mode=script harness=simulation_scenario -->
```python
from attest.simulation import scenario, FRIENDLY_USER

@scenario(
    persona=FRIENDLY_USER,
    mock_tools={"search_web": lambda q: {"results": ["mock result"]}},
    fault_error_rate=0.1,
    fault_latency_jitter_ms=200,
    max_turns=5,
    seed=42,
)
def test_multi_turn_support(persona):
    result = my_agent.run(persona=persona)
    return result
```

### ScenarioConfig Parameters

| Parameter                 | Type                  | Default | Description                                                  |
| ------------------------- | --------------------- | ------- | ------------------------------------------------------------ |
| `persona`                 | `Persona \| None`     | `None`  | Simulated user persona passed to the test function           |
| `mock_tools`              | `dict[str, Callable]` | `{}`    | Tool name to mock implementation mapping                     |
| `fault_error_rate`        | `float`               | `0.0`   | Probability (0.0-1.0) of injecting a `RuntimeError` per turn |
| `fault_latency_jitter_ms` | `int`                 | `0`     | Max random latency added per turn (milliseconds)             |
| `max_turns`               | `int`                 | `1`     | Number of turns to execute                                   |
| `seed`                    | `int \| None`         | `None`  | RNG seed for reproducible fault injection                    |

### ScenarioResult

The decorated function returns a `ScenarioResult`:

<!-- snippet: fragment reason="excerpt: relies on `test_multi_turn_support`, defined outside the snippet" -->
```python
result = test_multi_turn_support()

result.config     # ScenarioConfig used
result.results    # list[AgentResult] — one per turn
result.passed     # True if the aggregate outcome() is pass (soft failures allowed)
result.outcome()  # "fail" | "skip" | "pass" over all turns
```

### Multi-Turn Flow

When `max_turns > 1`, the scenario executes the wrapped function once per turn. Each turn produces an `AgentResult`. The persona is injected as a keyword argument if provided:

<!-- snippet: fragment reason="excerpt: relies on `scenario`, defined outside the snippet" -->
```python
@scenario(persona=ADVERSARIAL_USER, max_turns=3, seed=42)
def test_adversarial_multi_turn(persona):
    result = my_agent.run(input=persona.system_prompt)
    return result

scenario_result = test_adversarial_multi_turn()
assert scenario_result.passed
assert len(scenario_result.results) == 3
```

### Mock Tool Integration

Mock tools registered via `mock_tools` are activated as a context variable during scenario execution. When `TraceBuilder.add_tool_call()` is called with a tool name that matches a registered mock, the mock function is invoked instead:

<!-- snippet: fragment reason="excerpt: relies on `scenario`, defined outside the snippet" -->
```python
@scenario(mock_tools={
    "lookup_order": lambda order_id: {"status": "delivered", "total": 89.99},
    "process_refund": lambda order_id: {"refund_id": "RFD-001"},
})
def test_refund_with_mocks():
    builder = TraceBuilder(agent_id="refund-agent")
    builder.add_tool_call("lookup_order", args={"order_id": "ORD-123"})
    # Result is automatically populated by mock
    builder.set_output(message="Refund processed.")
    return AgentResult(trace=builder.build())
```

## `@repeat` Decorator

Run a test function multiple times and collect statistical results. Useful for measuring pass rates under non-deterministic conditions.

<!-- snippet: fragment reason="excerpt: relies on `my_agent`, defined outside the snippet" -->
```python
from attest import expect
from attest.simulation import repeat


def test_agent_reliability(attest):
    @repeat(n=10)
    def trial():
        result = my_agent.run("Process refund for ORD-123")
        return attest.inspect(expect(result).output_contains("ORD-123"))

    repeat_result = trial()
    repeat_result.count          # 10
    repeat_result.pass_rate      # passing / evaluated runs, or None
    repeat_result.not_evaluated  # runs skipped as not evaluated
    repeat_result.all_passed     # True if all 10 passed
    repeat_result.outcome()      # "fail" | "skip" | "pass" over all runs
    repeat_result.results        # list[AgentResult]
    attest.enforce(repeat_result)
```

Build each trial's result with `attest.inspect(chain)`, which never fails or skips the test, and enforce only the aggregate with `attest.enforce(repeat_result)` (see [Verdicts](verdicts.md)).

### RepeatResult Properties

| Property        | Type                | Description                                                              |
| --------------- | ------------------- | ------------------------------------------------------------------------ |
| `count`         | `int`               | Number of runs executed                                                  |
| `pass_rate`     | `float \| None`     | Passing / evaluated runs; not-evaluated runs excluded; `None` if none    |
| `not_evaluated` | `int`               | Runs whose outcome is skip (an assertion was not evaluated)              |
| `all_passed`    | `bool`              | True if every run's outcome is pass (soft failures allowed)              |
| `outcome()`     | `str`               | Any failing run → `fail`; else any skipped run → `skip`; else `pass`      |
| `results`       | `list[AgentResult]` | Individual results from each run                                         |

## `@mock_tool` Decorator

Tag a function as a mock tool implementation. The decorated function retains a `_mock_tool_name` attribute for registration:

<!-- snippet: harness=simulation_mock_tool -->
```python
from attest.simulation import mock_tool

@mock_tool("search_web")
def mock_search(q: str) -> dict:
    return {"results": [f"Mock result for: {q}"]}
```

### MockToolRegistry

For programmatic mock registration, use `MockToolRegistry` as a context manager:

<!-- snippet: fragment reason="excerpt: relies on `my_agent`, defined outside the snippet" -->
```python
from attest.simulation import MockToolRegistry

registry = MockToolRegistry()
registry.register("search_web", lambda q: {"results": ["mock"]})
registry.register("fetch_url", lambda url: {"content": "mock content"})

with registry:
    # All TraceBuilder.add_tool_call() invocations within this block
    # use the registered mocks
    result = my_agent.run("Search for testing frameworks")
```

## `@fault_inject` Decorator

Add random failures and latency to any function. Useful for testing agent resilience to tool failures:

<!-- snippet: harness=simulation_fault_inject -->
```python
from attest.simulation import fault_inject

@fault_inject(error_rate=0.3, latency_jitter_ms=500, seed=42)
def flaky_api_call(query: str) -> dict:
    return {"data": actual_api_call(query)}
```

### Parameters

| Parameter           | Type          | Default | Description                                     |
| ------------------- | ------------- | ------- | ----------------------------------------------- |
| `error_rate`        | `float`       | `0.0`   | Probability (0.0-1.0) of raising `RuntimeError` |
| `latency_jitter_ms` | `int`         | `0`     | Max random sleep added (milliseconds)           |
| `seed`              | `int \| None` | `None`  | RNG seed for reproducible behavior              |

When `error_rate > 0` and the RNG triggers, `RuntimeError(f"Injected fault in {fn.__name__}")` is raised before the function executes.

## Personas

Three built-in personas simulate different user interaction styles:

### FRIENDLY_USER

Cooperative user with clear, well-structured requests.

```python
from attest.simulation import FRIENDLY_USER

FRIENDLY_USER.name            # "friendly_user"
FRIENDLY_USER.style           # "friendly"
FRIENDLY_USER.temperature     # 0.7
FRIENDLY_USER.system_prompt   # "You are a friendly, cooperative user..."
```

### ADVERSARIAL_USER

Tests edge cases, sends malformed inputs, attempts to elicit unexpected behaviors.

```python
from attest.simulation import ADVERSARIAL_USER

ADVERSARIAL_USER.name          # "adversarial_user"
ADVERSARIAL_USER.style         # "adversarial"
ADVERSARIAL_USER.temperature   # 0.9
```

### CONFUSED_USER

Gives vague, contradictory instructions and frequently changes direction.

```python
from attest.simulation import CONFUSED_USER

CONFUSED_USER.name          # "confused_user"
CONFUSED_USER.style         # "confused"
CONFUSED_USER.temperature   # 0.8
```

### Custom Personas

Create custom personas with the `Persona` dataclass:

```python
from attest.simulation import Persona

IMPATIENT_USER = Persona(
    name="impatient_user",
    system_prompt="You are an impatient user who demands immediate answers and gets frustrated with follow-up questions.",
    style="impatient",
    temperature=0.85,
)
```

`Persona` is a frozen dataclass with four fields:

| Field           | Type    | Default  | Description                                    |
| --------------- | ------- | -------- | ---------------------------------------------- |
| `name`          | `str`   | required | Identifier for the persona                     |
| `system_prompt` | `str`   | required | System prompt describing the user behavior     |
| `style`         | `str`   | required | Short label for the interaction style          |
| `temperature`   | `float` | `0.7`    | LLM sampling temperature for persona responses |

## Composing Decorators

Decorators compose naturally. `@scenario` provides the simulation context; `@repeat` wraps it for statistical runs:

<!-- snippet: fragment reason="excerpt: relies on `my_agent`, defined outside the snippet" -->
```python
from attest import expect
from attest.simulation import scenario, repeat, ADVERSARIAL_USER


def test_adversarial_resilience(attest):
    @repeat(n=5)
    @scenario(persona=ADVERSARIAL_USER, max_turns=3, seed=42)
    def trial(persona):
        result = my_agent.run(input=persona.system_prompt)
        return attest.inspect(expect(result).output_not_contains("error"))

    # Each of 5 repeats runs the 3-turn adversarial scenario
    repeat_result = trial()
    # pass_rate is None when no repeat was evaluated (e.g. judge-only checks in simulation)
    assert repeat_result.pass_rate is not None and repeat_result.pass_rate >= 0.8
```

## Fault Injection Patterns

### Tool-Level Faults

Apply `@fault_inject` to individual tool implementations to simulate unreliable external services:

<!-- snippet: fragment reason="excerpt: relies on `fault_inject`, defined outside the snippet" -->
```python
@fault_inject(error_rate=0.5, latency_jitter_ms=1000, seed=42)
def unreliable_search(q: str) -> dict:
    return external_search_api(q)

@scenario(mock_tools={"search_web": unreliable_search})
def test_agent_handles_search_failures():
    result = my_agent.run("Find information about testing")
    return result
```

### Scenario-Level Faults

Use `fault_error_rate` and `fault_latency_jitter_ms` on `@scenario` to inject faults at the turn level rather than the tool level:

<!-- snippet: fragment reason="excerpt: relies on `scenario`, defined outside the snippet" -->
```python
@scenario(fault_error_rate=0.1, fault_latency_jitter_ms=300, max_turns=10, seed=42)
def test_multi_turn_resilience():
    result = my_agent.run("Long conversation")
    return result
```

## Best Practices

1. **Set seeds for reproducibility.** Both `@scenario` and `@fault_inject` accept a `seed` parameter. Use it in CI to ensure deterministic fault patterns.

2. **Choose a fixed trial count before running.** Use `@repeat` to measure pass rates; use [statistical verdicts](statistical-verdicts.md) to compare them with a baseline. A small sample may be inconclusive rather than proof of reliability.

3. **Start with FRIENDLY_USER, then escalate.** Validate basic behavior with cooperative input before testing adversarial and confused personas.

4. **Mock expensive tools.** Use `mock_tools` to avoid real API calls during simulation runs. Reserve integration tests for a separate suite.

5. **Combine with assertions.** Simulation decorators aggregate verdicts. Return `attest.inspect(chain)` from the decorated function so each trial carries its assertion results; in 1.1.0, an unevaluated chain fails the test:

<!-- snippet: fragment reason="excerpt: relies on `my_agent`, defined outside the snippet" -->
```python
from attest import expect
from attest.simulation import scenario, repeat, ADVERSARIAL_USER

def test_adversarial_with_assertions(attest):
    @repeat(n=10)
    @scenario(persona=ADVERSARIAL_USER, max_turns=3, seed=42)
    def trial(persona):
        result = my_agent.run(input=persona.system_prompt)
        return attest.inspect(expect(result).output_not_contains("error").cost_under(0.05))

    repeat_result = trial()
    assert repeat_result.pass_rate is not None and repeat_result.pass_rate >= 0.9
```

## TypeScript

`@attest-ai/core` has the same helpers as functions instead of decorators. Seeded faults are deterministic within TypeScript for a fixed seed (the random sequence differs from Python's). `scenario` and `repeat` return async functions.

<!-- snippet: fragment reason="excerpt: relies on `runAgent`, defined outside the snippet" -->
```ts
import {
  FRIENDLY_USER,
  MockToolRegistry,
  TraceBuilder,
  faultInject,
  mockTool,
  repeat,
  scenario,
} from "@attest-ai/core";
import { useAttest } from "@attest-ai/vitest";

// scenario: persona, mock tools, fault rate, jitter, turns, seed
const run = scenario(
  {
    persona: FRIENDLY_USER,
    mockTools: { search_web: () => ({ results: ["mock result"] }) },
    faultErrorRate: 0.1,
    faultLatencyJitterMs: 200,
    maxTurns: 5,
    seed: 42,
  },
  ({ persona, turn }) => runAgent(persona, turn), // returns an AgentResult
);
const result = await run(); // ScenarioResult: passed, outcome(), skipReason()
useAttest().enforce(result); // fail with the turn diagnostics, or skip with the not-evaluated reasons

// repeat: N trials aggregated as a RepeatResult
const trials = await repeat(10, run)();
trials.passRate; // passing / evaluated, or null
trials.outcome(); // "fail" | "skip" | "pass"
useAttest().enforce(trials); // enforce the aggregate only

// mockTool + MockToolRegistry: TraceBuilder.addToolCall returns the mock's result
const registry = new MockToolRegistry();
registry.register("search", mockTool("search", (args) => ({ mocked: args.query })));
const trace = registry.run(() =>
  new TraceBuilder("agent").addToolCall("search", { args: { query: "hi" } }).setOutput({ result: "ok" }).build(),
);

// faultInject: wrap any function
const flaky = faultInject({ errorRate: 0.5, latencyJitterMs: 50, seed: 1 }, fetchOrder);
```

| Python | TypeScript |
| --- | --- |
| `@scenario(persona=…, fault_error_rate=…)` | `scenario({ persona, faultErrorRate }, fn)`; `fn` receives `{ persona, turn }` |
| `@repeat(n)` | `repeat(n, fn)` |
| `@mock_tool(name)` / `MockToolRegistry` (`with`) | `mockTool(name, fn)` / `MockToolRegistry.run(fn)` |
| `@fault_inject(error_rate=…)` | `faultInject({ errorRate }, fn)` |

`enforce` (also exported as `enforceOutcome`) takes an `AgentResult`, a `ScenarioResult` or a `RepeatResult`, honors `ATTEST_STRICT` (or `{ strict: true }`) and does nothing inside a `trials()` trial, like Python's `attest.enforce`. Mock registries are scoped per async call chain (`AsyncLocalStorage`), so concurrent scenarios do not see each other's mocks. The full Python-to-TypeScript surface mapping is in the [parity matrix](parity.md).

### Plugins in TypeScript

An installed package declares its plugins in its own `package.json`, the TypeScript counterpart of Python's `attest.plugins` entry-point group:

```json
{ "name": "attest-plugin-safety", "attest": { "plugins": ["./dist/plugin.js"] } }
```

`loadPackagePlugins(registry, projectRoot?)` reads the project's direct dependencies (`dependencies`, `devDependencies`, `optionalDependencies`), imports each declared entry of every installed dependency and registers its default export (without one, every export with `name`, `pluginType` and `execute`). A broken entry is logged and skipped; the call returns the number of plugins loaded.

```ts
import { PluginRegistry, loadPackagePlugins } from "@attest-ai/core";

const registry = new PluginRegistry();
await loadPackagePlugins(registry); // projectRoot defaults to process.cwd()
registry.listPlugins(); // ["assertion/safety"]
```
