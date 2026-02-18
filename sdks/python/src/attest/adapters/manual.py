"""Manual trace adapter using TraceBuilder."""

from __future__ import annotations

from collections.abc import Callable

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class ManualAdapter:
    """Adapter for manually constructing traces via TraceBuilder."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def capture(self, builder_fn: Callable[[TraceBuilder], None]) -> Trace:
        """Execute builder_fn with a TraceBuilder and return the built Trace.

        builder_fn receives a TraceBuilder and should call methods on it
        to construct the trace. It does not need to call build().
        """
        builder = TraceBuilder(agent_id=self._agent_id)
        builder_fn(builder)
        return builder.build()

    def create_builder(self) -> TraceBuilder:
        """Create a new TraceBuilder for manual construction."""
        return TraceBuilder(agent_id=self._agent_id)
