"""Trace adapters for capturing agent execution traces."""

from __future__ import annotations

from typing import Any, Protocol

from attest._proto.types import Trace


class TraceAdapter(Protocol):
    """Protocol for trace capture adapters."""

    def capture(self, *args: Any, **kwargs: Any) -> Trace:
        """Capture an agent execution and return a Trace."""
        ...
