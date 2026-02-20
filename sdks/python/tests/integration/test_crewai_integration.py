"""Integration tests for CrewAI adapter with real framework objects.

CrewAI's Agent/Crew/Task constructors require OPENAI_API_KEY for LLM
initialization, so we use SimpleNamespace duck types that match the
attribute interface the adapter reads via getattr(). The adapter itself
never calls CrewAI methods — it only reads attributes.
"""

from __future__ import annotations

import types

import pytest

crewai_mod = pytest.importorskip("crewai")

from attest.adapters.crewai import CrewAIAdapter  # noqa: E402

pytestmark = pytest.mark.integration


def _make_agent(role: str) -> types.SimpleNamespace:
    """Build a duck-typed CrewAI Agent matching the adapter's getattr interface."""
    return types.SimpleNamespace(role=role, name=role)


def _make_crew(
    agents: list[types.SimpleNamespace],
    description: str = "",
) -> types.SimpleNamespace:
    """Build a duck-typed Crew matching the adapter's getattr interface."""
    return types.SimpleNamespace(agents=agents, description=description)


def _make_crew_output(
    *,
    raw: str = "",
    tasks_output: list[types.SimpleNamespace] | None = None,
    token_usage: dict[str, int] | None = None,
) -> types.SimpleNamespace:
    """Build a duck-typed CrewOutput matching the adapter's getattr interface."""
    return types.SimpleNamespace(
        raw=raw,
        tasks_output=tasks_output or [],
        token_usage=token_usage,
    )


class TestCrewAIAdapterIntegration:
    """Tests using duck-typed CrewAI objects against the Attest adapter.

    Validates that the adapter correctly reads attributes from objects
    matching the CrewAI interface without requiring API credentials.
    """

    def test_real_agent_objects_become_steps(self) -> None:
        """Agent objects with role attribute produce agent_call steps."""
        agent = _make_agent("researcher")
        crew = _make_crew([agent])

        adapter = CrewAIAdapter(agent_id="crew-test")
        with adapter.capture():
            pass  # Skip actual kickoff — testing adapter mapping only

        crew_output = _make_crew_output(raw="Research complete")
        trace = adapter.trace_from_crew_output(crew_output, crew)

        agent_steps = [s for s in trace.steps if s.type == "agent_call"]
        assert len(agent_steps) == 1
        assert agent_steps[0].name == "researcher"

    def test_real_crew_output_extraction(self) -> None:
        """CrewOutput raw text and token usage flow to trace output/metadata."""
        agent = _make_agent("writer")
        crew = _make_crew([agent])

        adapter = CrewAIAdapter(agent_id="crew-output")
        with adapter.capture():
            pass

        crew_output = _make_crew_output(
            raw="The final article content",
            token_usage={"total_tokens": 500},
        )
        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.output is not None
        assert trace.output["message"] == "The final article content"
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 500

    def test_real_task_output_in_trace(self) -> None:
        """TaskOutput objects produce tool_call steps."""
        agent = _make_agent("analyst")
        crew = _make_crew([agent])

        adapter = CrewAIAdapter(agent_id="crew-tasks")
        with adapter.capture():
            pass

        task_out = types.SimpleNamespace(
            description="Analyze the dataset",
            raw="Analysis complete: 95% accuracy",
        )
        crew_output = _make_crew_output(
            raw="Done",
            tasks_output=[task_out],
        )
        trace = adapter.trace_from_crew_output(crew_output, crew)

        tool_steps = [s for s in trace.steps if s.type == "tool_call"]
        assert len(tool_steps) == 1
        assert tool_steps[0].result is not None
        assert tool_steps[0].result["output"] == "Analysis complete: 95% accuracy"

    def test_crew_description_as_input(self) -> None:
        """Crew description flows to trace input."""
        agent = _make_agent("helper")
        crew = _make_crew([agent], description="Research crew for data analysis")

        adapter = CrewAIAdapter(agent_id="crew-desc")
        with adapter.capture():
            pass

        crew_output = _make_crew_output(raw="Completed")
        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.input is not None
        assert trace.input["description"] == "Research crew for data analysis"
