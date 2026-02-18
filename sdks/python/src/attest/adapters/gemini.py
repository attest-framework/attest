"""Google Gemini trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest._proto.types import Trace
from attest.trace import TraceBuilder


class GeminiAdapter:
    """Captures Google Gemini generate_content calls into Attest traces."""

    def __init__(self, agent_id: str | None = None) -> None:
        self._agent_id = agent_id

    def trace_from_response(
        self,
        response: Any,  # google.generativeai GenerateContentResponse
        input_text: str | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from a Gemini GenerateContentResponse.

        Args:
            response: Gemini GenerateContentResponse object.
            input_text: The text prompt sent to the API.
            **metadata: Additional trace metadata (cost_usd, latency_ms, model).
        """
        builder = TraceBuilder(agent_id=self._agent_id)

        if input_text:
            builder.set_input_dict({"text": input_text})

        completion_text = ""
        if hasattr(response, "text") and response.text is not None:
            completion_text = response.text
        elif hasattr(response, "candidates") and response.candidates:
            parts = response.candidates[0].content.parts
            completion_text = "".join(p.text for p in parts if hasattr(p, "text"))

        if hasattr(response, "candidates") and response.candidates:
            for part in response.candidates[0].content.parts:
                if hasattr(part, "function_call") and part.function_call:
                    fc = part.function_call
                    builder.add_tool_call(
                        name=fc.name,
                        args=dict(fc.args) if fc.args else {},
                    )

        builder.add_llm_call("completion", result={"completion": completion_text})
        builder.set_output_dict({"message": completion_text})

        builder.set_metadata(
            cost_usd=metadata.get("cost_usd"),
            latency_ms=metadata.get("latency_ms"),
            model=metadata.get("model"),
        )

        return builder.build()
