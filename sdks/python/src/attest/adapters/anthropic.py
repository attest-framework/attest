"""Anthropic trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class AnthropicAdapter:
    """Captures Anthropic Messages API calls into Attest traces."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def trace_from_response(
        self,
        response: Any,  # anthropic.types.Message — untyped external library
        input_messages: list[dict[str, Any]] | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from an Anthropic Messages response.

        Args:
            response: Anthropic Messages API response object.
            input_messages: The messages sent to the API.
            **metadata: Additional trace metadata (cost_usd, latency_ms).
        """
        builder = TraceBuilder(agent_id=self._agent_id)

        if input_messages:
            builder.set_input_dict({"messages": input_messages})

        completion_parts: list[str] = []
        for block in response.content:
            if block.type == "text":
                completion_parts.append(block.text)
            elif block.type == "tool_use":
                builder.add_tool_call(
                    name=block.name,
                    args=block.input if isinstance(block.input, dict) else {},
                )

        completion_text = "\n".join(completion_parts)

        step_args: dict[str, Any] = {}
        if hasattr(response, "model"):
            step_args["model"] = response.model

        builder.add_llm_call("completion", args=step_args, result={"completion": completion_text})

        builder.set_output_dict({"message": completion_text})

        total_tokens: int | None = None
        if hasattr(response, "usage"):
            total_tokens = response.usage.input_tokens + response.usage.output_tokens

        builder.set_metadata(
            total_tokens=total_tokens,
            cost_usd=metadata.get("cost_usd"),
            latency_ms=metadata.get("latency_ms"),
            model=getattr(response, "model", None),
        )

        return builder.build()
