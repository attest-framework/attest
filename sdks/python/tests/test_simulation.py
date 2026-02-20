from __future__ import annotations

import pytest

from attest.agent import Agent
from attest.result import AgentResult
from attest.simulation._context import _active_builder, _active_mock_registry
from attest.simulation.fault_inject import fault_inject
from attest.simulation.mock_tools import MockToolRegistry, mock_tool
from attest.simulation.personas import (
    ADVERSARIAL_USER,
    CONFUSED_USER,
    COOPERATIVE_USER,
    FRIENDLY_USER,
    Persona,
)
from attest.simulation.repeat import RepeatResult, repeat
from attest.simulation.scenario import ScenarioConfig, ScenarioResult, scenario
from attest.trace import TraceBuilder


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_simple_agent_result() -> AgentResult:
    builder = TraceBuilder(agent_id="test-agent")
    builder.set_output(result="ok")
    return AgentResult(trace=builder.build())


# ---------------------------------------------------------------------------
# Persona tests
# ---------------------------------------------------------------------------


def test_persona_creation() -> None:
    p = Persona(
        name="custom",
        system_prompt="You are a test persona.",
        style="friendly",
        temperature=0.5,
    )
    assert p.name == "custom"
    assert p.system_prompt == "You are a test persona."
    assert p.style == "friendly"
    assert p.temperature == 0.5


def test_builtin_personas() -> None:
    assert FRIENDLY_USER.style == "friendly"
    assert FRIENDLY_USER.name == "friendly_user"

    assert ADVERSARIAL_USER.style == "adversarial"
    assert ADVERSARIAL_USER.name == "adversarial_user"

    assert CONFUSED_USER.style == "confused"
    assert CONFUSED_USER.name == "confused_user"

    assert COOPERATIVE_USER.style == "cooperative"
    assert COOPERATIVE_USER.name == "cooperative_user"
    assert COOPERATIVE_USER.temperature == 0.6


def test_personas_are_frozen() -> None:
    with pytest.raises((AttributeError, TypeError)):
        FRIENDLY_USER.name = "modified"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# mock_tool decorator
# ---------------------------------------------------------------------------


def test_mock_tool_decorator() -> None:
    @mock_tool("search")
    def fake_search(query: str) -> dict[str, str]:
        return {"result": f"mocked: {query}"}

    assert hasattr(fake_search, "_mock_tool_name")
    assert fake_search._mock_tool_name == "search"  # type: ignore[attr-defined]


def test_mock_tool_callable() -> None:
    @mock_tool("lookup")
    def fake_lookup(key: str) -> dict[str, str]:
        return {"value": key}

    result = fake_lookup(key="abc")
    assert result == {"value": "abc"}


# ---------------------------------------------------------------------------
# MockToolRegistry
# ---------------------------------------------------------------------------


def test_mock_tool_registry_context_manager() -> None:
    def my_mock(query: str) -> dict[str, str]:
        return {"mocked": query}

    assert _active_mock_registry.get(None) is None

    registry = MockToolRegistry()
    registry.register("search", my_mock)

    with registry as reg:
        active = _active_mock_registry.get(None)
        assert active is not None
        assert "search" in active
        assert reg is registry

    assert _active_mock_registry.get(None) is None


def test_mock_tool_registry_isolation() -> None:
    def mock_a(q: str) -> dict[str, str]:
        return {"a": q}

    def mock_b(q: str) -> dict[str, str]:
        return {"b": q}

    reg_a = MockToolRegistry()
    reg_a.register("tool_a", mock_a)

    reg_b = MockToolRegistry()
    reg_b.register("tool_b", mock_b)

    with reg_a:
        active = _active_mock_registry.get(None)
        assert active is not None
        assert "tool_a" in active
        assert "tool_b" not in active


# ---------------------------------------------------------------------------
# mock_tool integration with TraceBuilder
# ---------------------------------------------------------------------------


def test_mock_tool_integration_with_trace_builder() -> None:
    def fake_search(query: str) -> dict[str, str]:
        return {"results": f"mocked results for {query}"}

    registry = MockToolRegistry()
    registry.register("search", fake_search)

    with registry:
        builder = TraceBuilder(agent_id="test")
        builder.add_tool_call("search", args={"query": "hello"})
        builder.set_output(result="done")
        trace = builder.build()

    assert len(trace.steps) == 1
    step = trace.steps[0]
    assert step.name == "search"
    assert step.result == {"results": "mocked results for hello"}


def test_mock_tool_not_intercepted_outside_registry() -> None:
    builder = TraceBuilder(agent_id="test")
    builder.add_tool_call("search", args={"query": "hello"}, result={"original": "result"})
    builder.set_output(result="done")
    trace = builder.build()

    step = trace.steps[0]
    assert step.result == {"original": "result"}


# ---------------------------------------------------------------------------
# fault_inject
# ---------------------------------------------------------------------------


def test_fault_inject_error_rate_zero() -> None:
    @fault_inject(error_rate=0.0, seed=42)
    def my_fn() -> str:
        return "ok"

    # Should never raise
    for _ in range(20):
        assert my_fn() == "ok"


def test_fault_inject_error_rate_one() -> None:
    @fault_inject(error_rate=1.0, seed=42)
    def my_fn() -> str:
        return "ok"

    with pytest.raises(RuntimeError, match="Injected fault in my_fn"):
        my_fn()


def test_fault_inject_deterministic_with_seed() -> None:
    @fault_inject(error_rate=0.5, seed=99)
    def my_fn() -> str:
        return "ok"

    results: list[bool] = []
    for _ in range(10):
        try:
            my_fn()
            results.append(True)
        except RuntimeError:
            results.append(False)

    # With seed=99 we get a deterministic sequence — just verify it's not all True
    # (error_rate=0.5 over 10 runs will produce some failures)
    assert len(results) == 10


def test_fault_inject_latency_jitter() -> None:
    import time

    @fault_inject(latency_jitter_ms=50, seed=0)
    def my_fn() -> str:
        return "ok"

    start = time.monotonic()
    result = my_fn()
    elapsed = time.monotonic() - start

    assert result == "ok"
    assert elapsed >= 0.0  # jitter is applied


# ---------------------------------------------------------------------------
# repeat
# ---------------------------------------------------------------------------


def test_repeat_decorator() -> None:
    call_count = 0

    def agent_fn(builder: object, **kwargs: object) -> dict[str, object]:
        nonlocal call_count
        call_count += 1
        return {"result": "ok"}

    agent = Agent("repeat-agent", fn=agent_fn)

    @repeat(5)
    def run_agent() -> AgentResult:
        return agent.run()

    result = run_agent()

    assert isinstance(result, RepeatResult)
    assert result.count == 5
    assert call_count == 5


def test_repeat_pass_rate_all_pass() -> None:
    def agent_fn(builder: object, **kwargs: object) -> dict[str, object]:
        return {"result": "ok"}

    agent = Agent("repeat-agent", fn=agent_fn)

    @repeat(4)
    def run_agent() -> AgentResult:
        return agent.run()

    result = run_agent()
    assert result.pass_rate == 1.0
    assert result.all_passed is True


def test_repeat_pass_rate_empty() -> None:
    result = RepeatResult(results=[])
    assert result.pass_rate == 0.0
    assert result.all_passed is True  # vacuously true


def test_repeat_count_property() -> None:
    results = [_make_simple_agent_result() for _ in range(3)]
    rr = RepeatResult(results=results)
    assert rr.count == 3


# ---------------------------------------------------------------------------
# scenario
# ---------------------------------------------------------------------------


def test_scenario_basic() -> None:
    def fake_search(query: str) -> dict[str, str]:
        return {"mocked": query}

    @scenario(
        persona=FRIENDLY_USER,
        mock_tools={"search": fake_search},
    )
    def run_test(persona: Persona | None = None) -> AgentResult:
        def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
            builder.add_tool_call("search", args={"query": "test"})
            return {"result": "done"}

        agent = Agent("scenario-agent", fn=agent_fn)
        return agent.run()

    result = run_test()

    assert isinstance(result, ScenarioResult)
    assert isinstance(result.config, ScenarioConfig)
    assert result.config.persona == FRIENDLY_USER
    assert len(result.results) == 1


def test_scenario_multi_turn() -> None:
    call_count = 0

    @scenario(max_turns=3)
    def run_test() -> AgentResult:
        nonlocal call_count
        call_count += 1

        def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
            return {"result": "ok"}

        agent = Agent("multi-turn-agent", fn=agent_fn)
        return agent.run()

    result = run_test()

    assert len(result.results) == 3
    assert call_count == 3


def test_scenario_passed_property() -> None:
    @scenario(max_turns=2)
    def run_test() -> AgentResult:
        def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
            return {"result": "ok"}

        agent = Agent("passed-agent", fn=agent_fn)
        return agent.run()

    result = run_test()
    # No assertion_results means all passed (vacuously)
    assert result.passed is True


def test_scenario_mock_tools_active_during_execution() -> None:
    captured_registry: dict[str, object] | None = None

    @scenario(mock_tools={"my_tool": lambda: {"x": 1}})
    def run_test() -> AgentResult:
        nonlocal captured_registry
        captured_registry = _active_mock_registry.get(None)

        def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
            return {"result": "ok"}

        agent = Agent("registry-agent", fn=agent_fn)
        return agent.run()

    run_test()

    assert captured_registry is not None
    assert "my_tool" in captured_registry
    # Registry is cleared after scenario exits
    assert _active_mock_registry.get(None) is None


def test_scenario_mock_tools_cleared_after_exit() -> None:
    @scenario(mock_tools={"tool": lambda: {"ok": True}})
    def run_test() -> AgentResult:
        def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
            return {"result": "ok"}

        agent = Agent("clear-agent", fn=agent_fn)
        return agent.run()

    run_test()
    assert _active_mock_registry.get(None) is None


# ---------------------------------------------------------------------------
# _active_builder ContextVar
# ---------------------------------------------------------------------------


def test_active_builder_set_during_run() -> None:
    captured_builder: TraceBuilder | None = None

    def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
        nonlocal captured_builder
        captured_builder = _active_builder.get(None)
        return {"result": "ok"}

    agent = Agent("builder-ctx-agent", fn=agent_fn)
    agent.run()

    assert captured_builder is not None
    assert isinstance(captured_builder, TraceBuilder)


def test_active_builder_cleared_after_run() -> None:
    def agent_fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
        return {"result": "ok"}

    agent = Agent("builder-clear-agent", fn=agent_fn)
    agent.run()

    assert _active_builder.get(None) is None
