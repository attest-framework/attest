"""Trace construction utilities for Attest."""

from __future__ import annotations

import uuid
from typing import Any

from attest._proto.types import Step, Trace, TraceMetadata


class TraceBuilder:
    """Fluent builder for constructing Trace objects."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._trace_id: str = f"trc_{uuid.uuid4().hex[:12]}"
        self._agent_id: str | None = agent_id
        self._input: dict[str, Any] | None = None
        self._steps: list[Step] = []
        self._output: dict[str, Any] | None = None
        self._metadata: TraceMetadata | None = None
        self._parent_trace_id: str | None = None

    def set_trace_id(self, trace_id: str) -> TraceBuilder:
        self._trace_id = trace_id
        return self

    def set_input(self, **kwargs: Any) -> TraceBuilder:
        self._input = kwargs
        return self

    def set_input_dict(self, input_data: dict[str, Any]) -> TraceBuilder:
        self._input = input_data
        return self

    def add_llm_call(
        self,
        name: str,
        args: dict[str, Any] | None = None,
        result: dict[str, Any] | None = None,
        metadata: dict[str, Any] | None = None,
        started_at_ms: int | None = None,
        ended_at_ms: int | None = None,
        agent_id: str | None = None,
        agent_role: str | None = None,
    ) -> TraceBuilder:
        self._steps.append(
            Step(
                type="llm_call",
                name=name,
                args=args,
                result=result,
                metadata=metadata,
                started_at_ms=started_at_ms,
                ended_at_ms=ended_at_ms,
                agent_id=agent_id,
                agent_role=agent_role,
            )
        )
        return self

    def add_tool_call(
        self,
        name: str,
        args: dict[str, Any] | None = None,
        result: dict[str, Any] | None = None,
        metadata: dict[str, Any] | None = None,
        started_at_ms: int | None = None,
        ended_at_ms: int | None = None,
        agent_id: str | None = None,
        agent_role: str | None = None,
    ) -> TraceBuilder:
        from attest.simulation._context import _active_mock_registry

        registry = _active_mock_registry.get(None)
        if registry is not None and name in registry:
            mock_result = registry[name](**(args or {}))
            if isinstance(mock_result, dict):
                result = mock_result
        self._steps.append(
            Step(
                type="tool_call",
                name=name,
                args=args,
                result=result,
                metadata=metadata,
                started_at_ms=started_at_ms,
                ended_at_ms=ended_at_ms,
                agent_id=agent_id,
                agent_role=agent_role,
            )
        )
        return self

    def add_retrieval(
        self,
        name: str,
        args: dict[str, Any] | None = None,
        result: dict[str, Any] | None = None,
        metadata: dict[str, Any] | None = None,
        started_at_ms: int | None = None,
        ended_at_ms: int | None = None,
        agent_id: str | None = None,
        agent_role: str | None = None,
    ) -> TraceBuilder:
        self._steps.append(
            Step(
                type="retrieval",
                name=name,
                args=args,
                result=result,
                metadata=metadata,
                started_at_ms=started_at_ms,
                ended_at_ms=ended_at_ms,
                agent_id=agent_id,
                agent_role=agent_role,
            )
        )
        return self

    def add_step(self, step: Step) -> TraceBuilder:
        self._steps.append(step)
        return self

    def set_output(self, **kwargs: Any) -> TraceBuilder:
        self._output = kwargs
        return self

    def set_output_dict(self, output_data: dict[str, Any]) -> TraceBuilder:
        self._output = output_data
        return self

    def set_metadata(
        self,
        total_tokens: int | None = None,
        cost_usd: float | None = None,
        latency_ms: int | None = None,
        model: str | None = None,
        timestamp: str | None = None,
    ) -> TraceBuilder:
        self._metadata = TraceMetadata(
            total_tokens=total_tokens,
            cost_usd=cost_usd,
            latency_ms=latency_ms,
            model=model,
            timestamp=timestamp,
        )
        return self

    def set_parent_trace_id(self, parent_id: str) -> TraceBuilder:
        self._parent_trace_id = parent_id
        return self

    def build(self) -> Trace:
        if self._output is None:
            raise ValueError("Trace output is required. Call set_output() before build().")
        return Trace(
            trace_id=self._trace_id,
            output=self._output,
            schema_version=1,
            agent_id=self._agent_id,
            input=self._input,
            steps=list(self._steps),
            metadata=self._metadata,
            parent_trace_id=self._parent_trace_id,
        )
