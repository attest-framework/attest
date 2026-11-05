"""Base adapter classes for Attest trace capture."""

from __future__ import annotations

import time
from abc import ABCMeta, abstractmethod
from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class BaseAdapter:
    """Root base class for all Attest adapters.

    Provides shared utilities used across both provider and framework adapters:
    builder creation, timestamp helpers, and agent_id storage.
    """

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def _create_builder(self) -> TraceBuilder:
        """Create a TraceBuilder pre-configured with this adapter's agent_id."""
        return TraceBuilder(agent_id=self._agent_id)

    def _now_ms(self) -> int:
        """Return current wall-clock time in milliseconds since epoch."""
        return int(time.time() * 1000)

    def _resolve_timestamps(
        self,
        started_at_ms: int | None,
        ended_at_ms: int | None,
    ) -> tuple[int, int]:
        """Resolve optional timestamps, falling back to current time.

        Args:
            started_at_ms: Wall-clock ms when request was sent.
            ended_at_ms: Wall-clock ms when response was received.

        Returns:
            Tuple of (started_at_ms, ended_at_ms) with fallbacks applied.
        """
        now = self._now_ms()
        return (
            started_at_ms if started_at_ms is not None else now,
            ended_at_ms if ended_at_ms is not None else now,
        )


class BaseProviderAdapter(BaseAdapter, metaclass=ABCMeta):
    """Template method base for single-LLM-call provider adapters.

    Subclasses override extraction methods to map provider-specific response
    objects into a unified Trace structure. The algorithm skeleton in
    ``trace_from_response`` handles timestamp resolution, builder setup,
    step construction, and metadata assembly.

    Required overrides:
        _extract_completion, _extract_model, _extract_total_tokens,
        _extract_tool_calls

    Optional overrides:
        _extract_input (default: wraps input_messages in {"messages": ...})
        _build_output (default: {"message": completion_text})
    """

    def trace_from_response(
        self,
        response: Any,
        input_messages: list[dict[str, Any]] | None = None,
        started_at_ms: int | None = None,
        ended_at_ms: int | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from an LLM provider response.

        Args:
            response: Provider-specific response object.
            input_messages: The messages sent to the API.
            started_at_ms: Wall-clock ms when the request was sent.
            ended_at_ms: Wall-clock ms when the response was received.
            **metadata: Additional trace metadata (cost_usd, latency_ms, etc.).
        """
        step_started, step_ended = self._resolve_timestamps(started_at_ms, ended_at_ms)
        builder = self._create_builder()

        # Input
        input_data = self._extract_input(input_messages, **metadata)
        if input_data is not None:
            builder.set_input_dict(input_data)

        # Core extraction
        completion_text = self._extract_completion(response)
        model = self._extract_model(response, **metadata)
        total_tokens = self._extract_total_tokens(response)
        input_tokens, output_tokens = self._extract_token_split(response)
        refusal = self._extract_refusal(response)

        # LLM call step
        step_args: dict[str, Any] = {}
        if model is not None:
            step_args["model"] = model

        step_result: dict[str, Any] = {"completion": completion_text}
        if refusal:
            step_result["refusal"] = refusal
        if total_tokens is not None:
            step_result["tokens"] = total_tokens
        if input_tokens is not None:
            step_result["input_tokens"] = input_tokens
        if output_tokens is not None:
            step_result["output_tokens"] = output_tokens

        builder.add_llm_call(
            "completion",
            args=step_args,
            result=step_result,
            started_at_ms=step_started,
            ended_at_ms=step_ended,
        )

        # Tool calls
        for tc in self._extract_tool_calls(response):
            builder.add_tool_call(name=tc["name"], args=tc.get("args"))

        # Output
        output = self._build_output(response, completion_text, **metadata)
        builder.set_output_dict(output)

        # Metadata
        builder.set_metadata(
            total_tokens=total_tokens,
            cost_usd=metadata.get("cost_usd"),
            latency_ms=self._latency_ms(started_at_ms, ended_at_ms, metadata),
            model=model,
        )

        return builder.build()

    def _extract_input(
        self,
        input_messages: list[dict[str, Any]] | None,
        **metadata: Any,
    ) -> dict[str, Any] | None:
        """Extract input data for the trace. Override for custom input formats."""
        if input_messages:
            return {"messages": input_messages}
        return None

    @abstractmethod
    def _extract_completion(self, response: Any) -> str:
        """Extract the completion text from the provider response."""
        ...

    @abstractmethod
    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        """Extract the model name from the provider response."""
        ...

    @abstractmethod
    def _extract_total_tokens(self, response: Any) -> int | None:
        """Extract total token count from the provider response."""
        ...

    def _extract_token_split(self, response: Any) -> tuple[int | None, int | None]:
        """Extract ``(input_tokens, output_tokens)``; ``None`` where the provider omits them."""
        return None, None

    def _extract_refusal(self, response: Any) -> str | None:
        """Extract the model's refusal text, if the provider reports one."""
        return None

    @staticmethod
    def _latency_ms(
        started_at_ms: int | None, ended_at_ms: int | None, metadata: dict[str, Any]
    ) -> int | None:
        """The caller's ``latency_ms``, else the span between the caller's timestamps."""
        explicit = metadata.get("latency_ms")
        if explicit is not None:
            return explicit  # type: ignore[no-any-return]
        if started_at_ms is not None and ended_at_ms is not None:
            return ended_at_ms - started_at_ms
        return None

    @abstractmethod
    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        """Extract tool calls from the provider response.

        Returns:
            List of dicts with "name" and optional "args" keys.
        """
        ...

    def _build_output(
        self,
        response: Any,
        completion_text: str,
        **metadata: Any,
    ) -> dict[str, Any]:
        """Build the output dict for the trace. Override for custom output formats."""
        return {"message": completion_text}
