"""Tests for multi-agent trace tree and delegate functionality."""

from __future__ import annotations

import pytest

from attest._proto.types import STEP_AGENT_CALL, Step, Trace, TraceMetadata
from attest.agent import Agent
from attest.delegate import delegate
from attest.expect import expect
from attest.result import AgentResult
from attest.trace import TraceBuilder
from attest.trace_tree import TraceTree


def _make_trace(
    trace_id: str,
    agent_id: str | None,
    steps: list[Step] | None = None,
    metadata: TraceMetadata | None = None,
    parent_trace_id: str | None = None,
) -> Trace:
    return Trace(
        trace_id=trace_id,
        output={"message": f"output of {agent_id or trace_id}"},
        agent_id=agent_id,
        steps=steps or [],
        metadata=metadata,
        parent_trace_id=parent_trace_id,
    )


def _make_agent_call_step(agent_id: str, sub_trace: Trace) -> Step:
    return Step(
        type=STEP_AGENT_CALL,
        name=agent_id,
        args=None,
        result=sub_trace.output,
        sub_trace=sub_trace,
    )


def test_trace_tree_single_agent() -> None:
    """TraceTree with no sub-agents: depth=0, agents=[root_id]."""
    root = _make_trace("trc_root", "root-agent")
    tree = TraceTree(root=root)

    assert tree.depth == 0
    assert tree.agents == ["root-agent"]
    assert tree.flatten() == [root]


def test_trace_tree_nested() -> None:
    """Build 3-level trace tree manually, verify agents(), depth, flatten()."""
    leaf = _make_trace("trc_leaf", "leaf-agent")
    middle = _make_trace(
        "trc_middle",
        "middle-agent",
        steps=[_make_agent_call_step("leaf-agent", leaf)],
    )
    root = _make_trace(
        "trc_root",
        "root-agent",
        steps=[_make_agent_call_step("middle-agent", middle)],
    )
    tree = TraceTree(root=root)

    assert tree.depth == 2
    assert tree.agents == ["root-agent", "middle-agent", "leaf-agent"]
    assert tree.flatten() == [root, middle, leaf]


def test_trace_tree_find_agent() -> None:
    """find_agent returns correct sub-trace, None for missing."""
    leaf = _make_trace("trc_leaf", "leaf-agent")
    root = _make_trace(
        "trc_root",
        "root-agent",
        steps=[_make_agent_call_step("leaf-agent", leaf)],
    )
    tree = TraceTree(root=root)

    found = tree.find_agent("leaf-agent")
    assert found is leaf

    assert tree.find_agent("nonexistent") is None
    assert tree.find_agent("root-agent") is root


def test_trace_tree_aggregate_tokens() -> None:
    """Sum total_tokens across tree."""
    leaf = _make_trace("trc_leaf", "leaf-agent", metadata=TraceMetadata(total_tokens=100))
    root = _make_trace(
        "trc_root",
        "root-agent",
        steps=[_make_agent_call_step("leaf-agent", leaf)],
        metadata=TraceMetadata(total_tokens=200),
    )
    tree = TraceTree(root=root)

    assert tree.aggregate_tokens == 300


def test_trace_tree_aggregate_cost() -> None:
    """Sum cost_usd across tree."""
    leaf = _make_trace("trc_leaf", "leaf-agent", metadata=TraceMetadata(cost_usd=0.005))
    root = _make_trace(
        "trc_root",
        "root-agent",
        steps=[_make_agent_call_step("leaf-agent", leaf)],
        metadata=TraceMetadata(cost_usd=0.010),
    )
    tree = TraceTree(root=root)

    assert abs(tree.aggregate_cost - 0.015) < 1e-9


def test_delegate_context_manager() -> None:
    """Use delegate() within Agent.run(), verify sub-trace added to parent."""

    def fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
        with delegate("sub-agent") as child:
            child.add_tool_call("search", args={"q": "test"})
            child.set_output(message="found it")
        return {"message": "done"}

    ag = Agent("root-agent", fn=fn)
    result = ag.run()

    trace = result.trace
    agent_call_steps = [s for s in trace.steps if s.type == STEP_AGENT_CALL]
    assert len(agent_call_steps) == 1

    step = agent_call_steps[0]
    assert step.name == "sub-agent"
    assert step.sub_trace is not None
    assert step.sub_trace.agent_id == "sub-agent"
    assert step.sub_trace.parent_trace_id == trace.trace_id


def test_delegate_nested() -> None:
    """Two levels of delegation produce correct nesting."""

    def fn(builder: TraceBuilder, **kwargs: object) -> dict[str, object]:
        with delegate("level-1") as child1:
            child1.add_tool_call("fetch", args={"url": "http://example.com"})
            with delegate("level-2") as child2:
                child2.add_tool_call("parse", args={"raw": "data"})
                child2.set_output(message="parsed")
            child1.set_output(message="fetched")
        return {"message": "done"}

    ag = Agent("root-agent", fn=fn)
    result = ag.run()

    trace = result.trace
    l1_steps = [s for s in trace.steps if s.type == STEP_AGENT_CALL]
    assert len(l1_steps) == 1

    l1_trace = l1_steps[0].sub_trace
    assert l1_trace is not None
    assert l1_trace.agent_id == "level-1"

    l2_steps = [s for s in l1_trace.steps if s.type == STEP_AGENT_CALL]
    assert len(l2_steps) == 1

    l2_trace = l2_steps[0].sub_trace
    assert l2_trace is not None
    assert l2_trace.agent_id == "level-2"


def test_delegate_outside_agent_raises() -> None:
    """delegate() without active builder raises RuntimeError."""
    with pytest.raises(RuntimeError, match="No active TraceBuilder found"):
        with delegate("sub-agent") as child:
            child.set_output(message="should not reach here")


def test_expect_agent_called() -> None:
    """ExpectChain.agent_called() creates correct assertion spec."""
    root = _make_trace("trc_root", "root-agent")
    result = AgentResult(trace=root)

    chain = expect(result).agent_called("sub-agent")
    assert len(chain.assertions) == 1
    a = chain.assertions[0]
    assert a.type == "trace_tree"
    assert a.spec["check"] == "agent_called"
    assert a.spec["agent_id"] == "sub-agent"
    assert a.spec["soft"] is False


def test_expect_delegation_depth() -> None:
    """ExpectChain.delegation_depth() creates correct assertion spec."""
    root = _make_trace("trc_root", "root-agent")
    result = AgentResult(trace=root)

    chain = expect(result).delegation_depth(3, soft=True)
    assert len(chain.assertions) == 1
    a = chain.assertions[0]
    assert a.type == "trace_tree"
    assert a.spec["check"] == "delegation_depth"
    assert a.spec["max_depth"] == 3
    assert a.spec["soft"] is True


def test_expect_cross_agent_data_flow() -> None:
    """ExpectChain.cross_agent_data_flow() creates correct assertion spec."""
    root = _make_trace("trc_root", "root-agent")
    result = AgentResult(trace=root)

    chain = expect(result).cross_agent_data_flow("agent-a", "agent-b", "order_id")
    assert len(chain.assertions) == 1
    a = chain.assertions[0]
    assert a.type == "trace_tree"
    assert a.spec["check"] == "cross_agent_data_flow"
    assert a.spec["from_agent"] == "agent-a"
    assert a.spec["to_agent"] == "agent-b"
    assert a.spec["field"] == "order_id"


def test_trace_tree_from_agent_result() -> None:
    """AgentResult.trace_tree() returns TraceTree with correct root."""
    root = _make_trace("trc_root", "root-agent")
    result = AgentResult(trace=root)

    tree = result.trace_tree()
    assert isinstance(tree, TraceTree)
    assert tree.root is root
