"""Tests for CrewAI adapter trace capture.

Demonstrates how the CrewAI adapter converts crew execution into
Attest traces for assertion evaluation. Uses manual trace building
so no crewai installation or OPENAI_API_KEY is needed.
"""

from __future__ import annotations

from agent import build_crew_trace_manually, run_crew
from attest import TraceTree
from attest.expect import expect
from attest.result import AgentResult


def test_crew_trace_captures_agents(attest) -> None:
    """Verify trace captures all crew agents as steps."""
    trace = build_crew_trace_manually()
    result = AgentResult(trace=trace)

    chain = (
        expect(result)
        # Output should contain the crew's raw output
        .output_contains("testing")
        # Trace should have steps from both agents + both tasks
        .step_count("gte", 2)
    )

    attest.evaluate(chain)


def test_crew_trace_has_agent_steps() -> None:
    """Verify trace structure from CrewAI adapter pattern."""
    trace = build_crew_trace_manually()

    # Trace should have input from crew description
    assert trace.input is not None
    assert "Research and writing crew" in str(trace.input)

    # Output should contain crew result
    assert trace.output is not None
    assert "testing" in str(trace.output).lower()

    # Steps should include agent_call entries for each agent
    agent_steps = [s for s in trace.steps if s.type == "agent_call"]
    assert len(agent_steps) == 2
    assert agent_steps[0].name == "researcher"
    assert agent_steps[1].name == "writer"

    # Tool call steps for task outputs
    tool_steps = [s for s in trace.steps if s.type == "tool_call"]
    assert len(tool_steps) == 2

    # Token metadata should be captured
    assert trace.metadata is not None
    assert trace.metadata.total_tokens == 800


def test_crew_trace_tree_analysis() -> None:
    """Demonstrate TraceTree analysis on CrewAI-style traces."""
    trace = build_crew_trace_manually()
    tree = TraceTree(root=trace)

    # The adapter creates a flat trace (depth 0) — agents are steps, not sub-traces
    assert tree.depth == 0
    assert "research-crew" in tree.agents


def test_run_crew_helper() -> None:
    """Verify the run_crew helper returns expected structure."""
    result = run_crew()
    assert "trace" in result
    assert "output" in result
    assert "testing" in result["output"].lower()
