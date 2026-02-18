"""Ollama trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class OllamaAdapter:
    """Captures Ollama chat calls into Attest traces."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def trace_from_response(
        self,
        response: dict[str, Any],
        input_messages: list[dict[str, Any]] | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from an Ollama chat response dict.

        Args:
            response: Ollama chat response dictionary.
            input_messages: The messages sent to the API.
            **metadata: Additional trace metadata (latency_ms).
        """
        builder = TraceBuilder(agent_id=self._agent_id)

        if input_messages:
            builder.set_input_dict({"messages": input_messages})

        message = response.get("message", {})
        completion_text: str = message.get("content", "")

        builder.add_llm_call(
            "completion",
            args={"model": response.get("model", "")},
            result={"completion": completion_text},
        )

        builder.set_output_dict({"message": completion_text})

        total_tokens: int | None = None
        if "eval_count" in response and "prompt_eval_count" in response:
            total_tokens = response["eval_count"] + response["prompt_eval_count"]

        builder.set_metadata(
            total_tokens=total_tokens,
            latency_ms=metadata.get("latency_ms"),
            model=response.get("model"),
        )

        return builder.build()
