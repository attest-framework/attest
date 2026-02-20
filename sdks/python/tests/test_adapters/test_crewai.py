"""Tests for the CrewAIAdapter."""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass, field
from unittest.mock import patch

import pytest

from attest._proto.types import STEP_AGENT_CALL, STEP_TOOL_CALL
from attest.adapters.crewai import CrewAIAdapter


# ---------------------------------------------------------------------------
# Mock CrewAI structures (crewai may not be installed in CI)
# ---------------------------------------------------------------------------


@dataclass
class MockTokenUsage:
    total_tokens: int = 0


@dataclass
class MockTaskOutput:
    description: str = "task"
    raw: str = ""


@dataclass
class MockAgent:
    role: str = "agent"


@dataclass
class MockCrewOutput:
    raw: str = ""
    tasks_output: list[MockTaskOutput] = field(default_factory=list)
    token_usage: MockTokenUsage | None = None


@dataclass
class MockCrew:
    agents: list[MockAgent] = field(default_factory=list)
    description: str = ""


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


@contextmanager
def _crewai_available() -> Generator[None, None, None]:
    """Patch _require_crewai to no-op (simulates crewai installed)."""
    with patch("attest.adapters.crewai._require_crewai"):
        yield


# ---------------------------------------------------------------------------
# Import guard
# ---------------------------------------------------------------------------


class TestCrewAIAdapterImportGuard:
    def test_raises_import_error_when_crewai_missing(self) -> None:
        with pytest.raises(ImportError, match="Install CrewAI extras"):
            CrewAIAdapter()


# ---------------------------------------------------------------------------
# trace_from_crew_output
# ---------------------------------------------------------------------------


class TestCrewAIAdapterTraceFromCrewOutput:
    def test_basic_trace_structure(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter(agent_id="test-crew")

        crew_output = MockCrewOutput(raw="Final answer")
        crew = MockCrew()

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace is not None
        assert trace.agent_id == "test-crew"
        assert trace.output["message"] == "Final answer"

    def test_agents_become_agent_call_steps(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter(agent_id="crew")

        crew_output = MockCrewOutput(raw="done")
        crew = MockCrew(agents=[MockAgent(role="researcher"), MockAgent(role="writer")])

        trace = adapter.trace_from_crew_output(crew_output, crew)

        agent_steps = [s for s in trace.steps if s.type == STEP_AGENT_CALL]
        assert len(agent_steps) == 2
        assert agent_steps[0].name == "researcher"
        assert agent_steps[1].name == "writer"

    def test_tasks_output_become_tool_call_steps(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        task1 = MockTaskOutput(description="research task", raw="research result")
        task2 = MockTaskOutput(description="write task", raw="written content")
        crew_output = MockCrewOutput(raw="done", tasks_output=[task1, task2])
        crew = MockCrew()

        trace = adapter.trace_from_crew_output(crew_output, crew)

        tool_steps = [s for s in trace.steps if s.type == STEP_TOOL_CALL]
        assert len(tool_steps) == 2
        assert tool_steps[0].name == "research task"
        assert tool_steps[0].result == {"output": "research result"}
        assert tool_steps[1].name == "write task"

    def test_token_usage_dict_extracted(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        crew_output = MockCrewOutput(
            raw="answer",
            token_usage=MockTokenUsage(total_tokens=250),
        )
        crew = MockCrew()

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 250

    def test_token_usage_dict_format(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        crew_output = MockCrewOutput(raw="answer")
        crew_output.token_usage = {"total_tokens": 100}  # type: ignore[assignment]
        crew = MockCrew()

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 100

    def test_no_token_usage_gives_none_metadata(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        crew_output = MockCrewOutput(raw="answer", token_usage=None)
        crew = MockCrew()

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.metadata is not None
        assert trace.metadata.total_tokens is None

    def test_crew_description_set_as_input(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        crew_output = MockCrewOutput(raw="result")
        crew = MockCrew(description="Research crew")

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.input is not None
        assert trace.input["description"] == "Research crew"

    def test_trace_stored_on_adapter(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter()

        assert adapter.trace is None

        crew_output = MockCrewOutput(raw="done")
        crew = MockCrew()
        returned_trace = adapter.trace_from_crew_output(crew_output, crew)

        assert adapter.trace is returned_trace

    def test_capture_context_manager_yields_self(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter(agent_id="cm-test")

        with adapter.capture() as cap:
            assert cap is adapter

    def test_combined_agents_and_tasks(self) -> None:
        with _crewai_available():
            adapter = CrewAIAdapter(agent_id="full-crew")

        task = MockTaskOutput(description="analyze", raw="analysis done")
        crew_output = MockCrewOutput(
            raw="Final report",
            tasks_output=[task],
            token_usage=MockTokenUsage(total_tokens=500),
        )
        crew = MockCrew(agents=[MockAgent(role="analyst")])

        trace = adapter.trace_from_crew_output(crew_output, crew)

        assert trace.agent_id == "full-crew"
        assert trace.output["message"] == "Final report"

        agent_steps = [s for s in trace.steps if s.type == STEP_AGENT_CALL]
        tool_steps = [s for s in trace.steps if s.type == STEP_TOOL_CALL]

        assert len(agent_steps) == 1
        assert agent_steps[0].name == "analyst"
        assert len(tool_steps) == 1
        assert tool_steps[0].name == "analyze"

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 500
