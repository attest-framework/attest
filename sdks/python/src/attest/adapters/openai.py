"""OpenAI trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class OpenAIAdapter:
    """Captures OpenAI ChatCompletion calls into Attest traces."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def trace_from_response(
        self,
        response: Any,  # openai.types.chat.ChatCompletion — untyped external library
        input_messages: list[dict[str, Any]] | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from an OpenAI ChatCompletion response.

        Args:
            response: OpenAI ChatCompletion response object.
            input_messages: The messages sent to the API.
            **metadata: Additional trace metadata (cost_usd, latency_ms, structured_output).
        """
        builder = TraceBuilder(agent_id=self._agent_id)

        if input_messages:
            builder.set_input_dict({"messages": input_messages})

        message = response.choices[0].message
        completion_text: str = message.content or ""

        step_args: dict[str, Any] = {}
        if hasattr(response, "model"):
            step_args["model"] = response.model

        step_result: dict[str, Any] = {"completion": completion_text}
        if hasattr(response, "usage") and response.usage:
            step_result["tokens"] = response.usage.total_tokens

        builder.add_llm_call("completion", args=step_args, result=step_result)

        if hasattr(message, "tool_calls") and message.tool_calls:
            for tc in message.tool_calls:
                builder.add_tool_call(
                    name=tc.function.name,
                    args={"arguments": tc.function.arguments},
                )

        builder.set_output_dict({
            "message": completion_text,
            "structured": metadata.get("structured_output", {}),
        })

        total_tokens: int | None = None
        if hasattr(response, "usage") and response.usage:
            total_tokens = response.usage.total_tokens

        builder.set_metadata(
            total_tokens=total_tokens,
            cost_usd=metadata.get("cost_usd"),
            latency_ms=metadata.get("latency_ms"),
            model=getattr(response, "model", None),
        )

        return builder.build()
