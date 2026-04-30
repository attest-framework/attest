"""Protocol diagnostics for client-side observability.

The reader loop emits a ``ProtocolDiagnostic`` whenever it cannot route a
response line (malformed JSON, missing id, unknown id, or non-routable
protocol error). Diagnostics are buffered in a bounded ring (32 entries
by default). When more than ``desync_threshold`` diagnostics appear
within ``desync_window_seconds`` the client raises
:class:`ProtocolDesyncError` to fail every in-flight request.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from typing import Literal, Protocol

ProtocolDiagnosticKind = Literal[
    "malformed_json",
    "non_object_response",
    "invalid_jsonrpc_version",
    "missing_id",
    "unknown_id",
    "non_routable_error",
]

DEFAULT_BUFFER_SIZE: int = 32
DEFAULT_DESYNC_THRESHOLD: int = 3
DEFAULT_DESYNC_WINDOW_SECONDS: float = 1.0
MAX_LINE_PREVIEW: int = 512


@dataclass(frozen=True, slots=True)
class ProtocolDiagnostic:
    """A single observability record for a non-routable protocol line."""

    kind: ProtocolDiagnosticKind
    message: str
    raw_line: str
    timestamp: float


class ProtocolLogger(Protocol):
    """Minimal logger protocol used by AttestClient.

    Compatible with :class:`logging.Logger` and ad-hoc duck-typed objects.
    """

    def warning(self, msg: str, *args: object) -> None: ...
    def error(self, msg: str, *args: object) -> None: ...


class ProtocolDiagnosticBuffer:
    """Bounded ring buffer for protocol diagnostics."""

    def __init__(self, capacity: int = DEFAULT_BUFFER_SIZE) -> None:
        if capacity <= 0:
            raise ValueError(f"ProtocolDiagnosticBuffer capacity must be positive, got {capacity}")
        self._entries: deque[ProtocolDiagnostic] = deque(maxlen=capacity)

    def push(self, diagnostic: ProtocolDiagnostic) -> None:
        self._entries.append(diagnostic)

    def snapshot(self) -> tuple[ProtocolDiagnostic, ...]:
        return tuple(self._entries)

    def count_within(self, window_seconds: float, now: float) -> int:
        cutoff = now - window_seconds
        count = 0
        for entry in reversed(self._entries):
            if entry.timestamp >= cutoff:
                count += 1
            else:
                break
        return count

    def clear(self) -> None:
        self._entries.clear()


def preview_line(line: str) -> str:
    """Truncate long lines for safer logging and storage."""
    if len(line) <= MAX_LINE_PREVIEW:
        return line
    return line[:MAX_LINE_PREVIEW] + "…"
