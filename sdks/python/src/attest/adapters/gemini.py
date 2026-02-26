"""Google Gemini trace capture adapter."""

from __future__ import annotations

import warnings
from typing import Any

from attest._proto.types import Trace
from attest.adapters._base import BaseProviderAdapter


class GeminiAdapter(BaseProviderAdapter):
    """Captures Google Gemini generate_content calls into Attest traces."""

    def trace_from_response(
        self,
        response: Any,
        input_messages: list[dict[str, Any]] | None = None,
        started_at_ms: int | None = None,
        ended_at_ms: int | None = None,
        **metadata: Any,
    ) -> Trace:
        """Build a Trace from a Gemini GenerateContentResponse.

        Accepts both ``input_messages`` (standard) and the deprecated
        ``input_text`` kwarg for backward compatibility.

        Args:
            response: Gemini GenerateContentResponse object.
            input_messages: The messages sent to the API.
            started_at_ms: Wall-clock ms when the request was sent.
            ended_at_ms: Wall-clock ms when the response was received.
            **metadata: Additional trace metadata (cost_usd, latency_ms, model).
                Use ``input_text`` (deprecated) to pass a plain text prompt.
        """
        if "input_text" in metadata:
            warnings.warn(
                "GeminiAdapter: 'input_text' is deprecated, use 'input_messages' instead. "
                "Pass input_messages=[{'role': 'user', 'content': text}].",
                DeprecationWarning,
                stacklevel=2,
            )
            if input_messages is None:
                input_messages = [{"role": "user", "content": metadata.pop("input_text")}]
            else:
                metadata.pop("input_text")

        return super().trace_from_response(
            response,
            input_messages=input_messages,
            started_at_ms=started_at_ms,
            ended_at_ms=ended_at_ms,
            **metadata,
        )

    def _extract_input(
        self,
        input_messages: list[dict[str, Any]] | None,
        **metadata: Any,
    ) -> dict[str, Any] | None:
        if input_messages:
            # Gemini historically used {"text": ...} format for single text inputs
            if (
                len(input_messages) == 1
                and "content" in input_messages[0]
                and isinstance(input_messages[0]["content"], str)
            ):
                return {"text": input_messages[0]["content"]}
            return {"messages": input_messages}
        return None

    def _extract_completion(self, response: Any) -> str:
        if hasattr(response, "text") and response.text is not None:
            return response.text  # type: ignore[no-any-return]
        if hasattr(response, "candidates") and response.candidates:
            parts = response.candidates[0].content.parts
            return "".join(p.text for p in parts if hasattr(p, "text"))
        return ""

    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        return metadata.get("model")

    def _extract_total_tokens(self, response: Any) -> int | None:
        usage = getattr(response, "usage_metadata", None)
        if usage is None:
            return None
        total = getattr(usage, "total_token_count", None)
        if total is not None:
            return int(total)
        prompt = getattr(usage, "prompt_token_count", None)
        candidates = getattr(usage, "candidates_token_count", None)
        if prompt is not None and candidates is not None:
            return int(prompt) + int(candidates)
        if prompt is not None:
            return int(prompt)
        return None

    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        if not hasattr(response, "candidates") or not response.candidates:
            return []
        calls: list[dict[str, Any]] = []
        for part in response.candidates[0].content.parts:
            if hasattr(part, "function_call") and part.function_call:
                fc = part.function_call
                calls.append({
                    "name": fc.name,
                    "args": dict(fc.args) if fc.args else {},
                })
        return calls
