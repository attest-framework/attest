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

    def test_real_agent_objects_are_not_recorded_as_steps(self) -> None:
        """A configured agent is not evidence it ran: no agent_call step."""
        agent = _make_agent("researcher")
        crew = _make_crew([agent])

        adapter = CrewAIAdapter(agent_id="crew-test")
        with adapter.capture():
            pass  # Skip actual kickoff — testing adapter mapping only

        crew_output = _make_crew_output(raw="Research complete")
        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.steps == []

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

    def test_real_task_output_is_not_a_tool_call(self) -> None:
        """TaskOutput objects do not become tool_call steps."""
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

        assert trace.steps == []

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
