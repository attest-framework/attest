from __future__ import annotations

from collections.abc import Callable
from contextvars import ContextVar
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from attest.trace import TraceBuilder

_active_mock_registry: ContextVar[dict[str, Callable[..., Any]] | None] = ContextVar(
    "_active_mock_registry", default=None
)
_active_builder: ContextVar[TraceBuilder | None] = ContextVar("_active_builder", default=None)
