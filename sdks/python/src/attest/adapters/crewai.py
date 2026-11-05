"""CrewAI adapter for Attest."""

from __future__ import annotations

import time
from collections.abc import Generator
from contextlib import contextmanager
from typing import Any

from attest._proto.types import Trace
from attest.adapters._base import BaseAdapter


def _require_crewai() -> None:
    """Raise ImportError if crewai is not installed."""
    try:
        import crewai  # noqa: F401
    except ImportError:
        raise ImportError("Install CrewAI extras: uv add 'attest-ai[crewai]'")


class CrewAIAdapter(BaseAdapter):
    """Maps a CrewAI Crew run to a summary-only Attest trace.

    The trace carries the crew's final output, token usage and latency. It has
    no steps: CrewAI reports which agents and tools ran through its event bus,
    not through ``CrewOutput``, and an agent or task listed in the crew's
    configuration may never have run. Assert on ``output``, ``total_tokens`` and
    ``latency_ms``; to assert on tool calls, capture the run through
    OpenTelemetry instead.

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
        """Build a summary-only Trace from a CrewAI CrewOutput.

        Maps:
        - crew.description → input (when the crew has one)
        - crew_output.raw → output message
        - crew_output.token_usage → metadata.total_tokens
        - time since ``capture()`` → metadata.latency_ms

        Args:
            crew_output: CrewAI CrewOutput object returned by crew.kickoff().
            crew: CrewAI Crew object; only its description is read.

        Returns:
            Populated Attest Trace.
        """
        builder = self._create_builder()

        # Input: record the crew description if available
        crew_description: str = getattr(crew, "description", "") or ""
        if crew_description:
            builder.set_input_dict({"description": crew_description})

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
