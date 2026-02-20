"""Trace adapters for capturing agent execution traces."""

from __future__ import annotations

from typing import Any, Protocol

from attest._proto.types import Trace
from attest.adapters._base import BaseAdapter, BaseProviderAdapter


class TraceAdapter(Protocol):
    """Protocol for trace capture adapters.

    .. deprecated::
        Use ``BaseAdapter`` (ABC) for concrete implementations instead.
    """

    def capture(self, *args: Any, **kwargs: Any) -> Trace:
        """Capture an agent execution and return a Trace."""
        ...


class ProviderAdapter(Protocol):
    """Protocol for provider-level trace adapters (single LLM call capture).

    .. deprecated::
        Use ``BaseProviderAdapter`` (ABC) for concrete implementations instead.
    """

    def trace_from_response(
        self,
        response: Any,
        input_messages: list[dict[str, Any]] | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from an LLM provider response."""
        ...


class FrameworkAdapter(Protocol):
    """Protocol for framework-level trace adapters (agent orchestration capture).

    .. deprecated::
        Use ``BaseAdapter`` (ABC) for concrete implementations instead.
    """

    def trace_from_events(
        self,
        events: list[Any],
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from framework orchestration events."""
        ...


__all__ = [
    # Base classes (extensibility surface)
    "BaseAdapter",
    "BaseProviderAdapter",
    # Protocols (deprecated — retained for backward compatibility)
    "TraceAdapter",
    "ProviderAdapter",
    "FrameworkAdapter",
    # Framework adapters
    "CrewAIAdapter",
]
