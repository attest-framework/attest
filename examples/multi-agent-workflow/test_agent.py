"""Tests demonstrating Layer 8 — Multi-Agent Trace Tree assertions.

Layer 8 provides cross-agent analysis via TraceTree:
- agent_called(): verify sub-agents were invoked
- delegation_depth(): limit nesting depth
- follows_transitions(): verify delegation chains
- aggregate_cost_under(): budget across all agents
- cross_agent_data_flow(): verify data passes between agents
"""

from __future__ import annotations

from agent import research_pipeline
from attest.expect import expect


def test_research_pipeline_delegation(attest) -> None:
    """Verify the orchestrator delegates to researcher and writer."""
    result = research_pipeline(topic="AI agent testing frameworks")

    chain = (
        expect(result)
        # Layer 8: Trace tree assertions
        .agent_called("researcher")
        .agent_called("writer")
        # Verify delegation chain follows expected transitions
        .follows_transitions([
            ("orchestrator", "researcher"),
            ("orchestrator", "writer"),
        ])
        # Nesting depth should be at most 1 (orchestrator -> sub-agent)
        .delegation_depth(2)
    )

    attest.evaluate(chain)


def test_research_pipeline_cost_budget(attest) -> None:
    """Verify aggregate cost across all agents stays within budget."""
    result = research_pipeline(topic="AI agent testing")

    chain = (
        expect(result)
        # Layer 8: Aggregate cost across entire trace tree
        .aggregate_cost_under(0.10)
        .aggregate_tokens_under(1000)
        # Layer 2: Orchestrator's own cost
        .cost_under(0.05)
    )

    attest.evaluate(chain)


def test_researcher_output_quality(attest) -> None:
    """Verify the researcher sub-agent produces useful output."""
    result = research_pipeline(topic="AI testing")

    chain = (
        expect(result)
        # Layer 8: Check sub-agent output content
        .agent_output_contains("researcher", "testing")
        # Layer 8: Verify data flows from researcher to writer
        .cross_agent_data_flow("researcher", "writer", "research")
        # Layer 4: Final output should reflect research
        .output_contains("testing")
    )

    attest.evaluate(chain)


def test_trace_tree_structure() -> None:
    """Verify TraceTree properties directly (no engine needed)."""
    from attest.trace_tree import TraceTree

    result = research_pipeline(topic="multi-agent systems")
    tree = TraceTree(root=result.trace)

    # Tree structure
    assert "orchestrator" in tree.agents
    assert "researcher" in tree.agents
    assert "writer" in tree.agents
    assert len(tree.agents) == 3

    # Delegation pairs
    delegations = tree.delegations
    assert ("orchestrator", "researcher") in delegations
    assert ("orchestrator", "writer") in delegations

    # Depth: orchestrator -> {researcher, writer} = depth 1
    assert tree.depth == 1

    # Flatten returns all traces
    all_traces = tree.flatten()
    assert len(all_traces) == 3

    # Aggregate metrics sum across all agents
    assert tree.aggregate_tokens == 650  # 100 + 300 + 250
    assert tree.aggregate_cost > 0
    assert tree.aggregate_latency > 0

    # Tool calls across entire tree
    tool_calls = tree.all_tool_calls()
    tool_names = [tc.name for tc in tool_calls]
    assert "web_search" in tool_names
    assert "arxiv_search" in tool_names
    assert "format_markdown" in tool_names

    # Find specific agent's trace
    researcher_trace = tree.find_agent("researcher")
    assert researcher_trace is not None
    assert researcher_trace.agent_id == "researcher"
