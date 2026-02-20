"""CrewAI adapter for Attest."""

from __future__ import annotations

import time
from collections.abc import Generator
from contextlib import contextmanager
from typing import Any

from attest._proto.types import STEP_AGENT_CALL, Step, Trace
from attest.adapters._base import BaseAdapter


def _require_crewai() -> None:
    """Raise ImportError if crewai is not installed."""
    try:
        import crewai  # noqa: F401
    except ImportError:
        raise ImportError("Install CrewAI extras: uv add 'attest-ai[crewai]'")


class CrewAIAdapter(BaseAdapter):
    """Maps CrewAI Crew execution to Attest traces.

    Crew → root trace, each Agent → sub-trace step (agent_call type).

    Usage::

        adapter = CrewAIAdapter(agent_id="my-crew")
        with adapter.capture() as cap:
            result = crew.kickoff()
        trace = cap.trace_from_crew_output(result, crew)
        full_trace = adapter.trace
    """

    def __init__(self, agent_id: str | None = None) -> None:
        super().__init__(agent_id=agent_id)
        _require_crewai()
        self._trace: Trace | None = None
        self._start_time: float | None = None

    @property
    def trace(self) -> Trace | None:
        return self._trace

    @contextmanager
    def capture(self) -> Generator[CrewAIAdapter, None, None]:
        """Context manager for trace capture.

        Yields self so callers can invoke trace_from_crew_output after kickoff.
        """
        self._start_time = time.monotonic()
        yield self

    def trace_from_crew_output(self, crew_output: Any, crew: Any) -> Trace:
        """Build a Trace from a CrewAI CrewOutput and Crew objects.

        Maps:
        - crew.agents → sub-trace steps (agent_call type)
        - crew_output.tasks_output → tool_call steps per task
        - crew_output.raw → output message
        - crew_output.token_usage → metadata

        Args:
            crew_output: CrewAI CrewOutput object returned by crew.kickoff().
            crew: CrewAI Crew object whose agents are recorded as agent_call steps.

        Returns:
            Populated Attest Trace.
        """
        builder = self._create_builder()
        started_at_ms = self._now_ms()

        # Input: record the crew description if available
        crew_description: str = getattr(crew, "description", "") or ""
        if crew_description:
            builder.set_input_dict({"description": crew_description})

        # Agent steps: each Agent in the Crew → agent_call step
        agents: list[Any] = getattr(crew, "agents", []) or []
        for agent_obj in agents:
            agent_name: str = str(
                getattr(agent_obj, "role", "") or getattr(agent_obj, "name", "agent")
            )
            builder.add_step(
                Step(
                    type=STEP_AGENT_CALL,
                    name=agent_name,
                    started_at_ms=started_at_ms,
                    ended_at_ms=started_at_ms,
                    agent_id=self._agent_id,
                )
            )

        # Task output steps: each TaskOutput → tool_call step
        tasks_output: list[Any] = getattr(crew_output, "tasks_output", []) or []
        for task_out in tasks_output:
            task_description: str = str(
                getattr(task_out, "description", "") or getattr(task_out, "name", "task")
            )
            task_raw: str = str(getattr(task_out, "raw", "") or "")
            builder.add_tool_call(
                name=task_description,
                result={"output": task_raw} if task_raw else None,
                started_at_ms=started_at_ms,
                ended_at_ms=started_at_ms,
                agent_id=self._agent_id,
            )

        # Extract token usage
        token_usage: Any = getattr(crew_output, "token_usage", None)
        total_tokens: int | None = None
        if token_usage is not None:
            if isinstance(token_usage, dict):
                raw_total = token_usage.get("total_tokens")
            else:
                raw_total = getattr(token_usage, "total_tokens", None)
            if raw_total is not None:
                total_tokens = int(raw_total)

        # Output
        raw_output: str = str(getattr(crew_output, "raw", "") or "")
        builder.set_output(message=raw_output)

        # Latency
        latency_ms: int | None = None
        if self._start_time is not None:
            latency_ms = int((time.monotonic() - self._start_time) * 1000)

        builder.set_metadata(
            total_tokens=total_tokens,
            latency_ms=latency_ms,
        )

        self._trace = builder.build()
        return self._trace
