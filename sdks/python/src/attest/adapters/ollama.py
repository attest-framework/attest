"""Ollama trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest.adapters._base import BaseProviderAdapter


class OllamaAdapter(BaseProviderAdapter):
    """Captures Ollama chat calls into Attest traces."""

    def _extract_completion(self, response: Any) -> str:
        message = response.get("message", {})
        return message.get("content", "")  # type: ignore[no-any-return]

    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        return response.get("model")  # type: ignore[no-any-return]

    def _extract_total_tokens(self, response: Any) -> int | None:
        if "eval_count" in response and "prompt_eval_count" in response:
            return response["eval_count"] + response["prompt_eval_count"]  # type: ignore[no-any-return]
        return None

    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        message = response.get("message", {})
        tool_calls = message.get("tool_calls")
        if not tool_calls:
            return []
        result = []
        for tc in tool_calls:
            fn = tc.get("function", {})
            result.append({
                "name": fn.get("name", ""),
                "args": fn.get("arguments", {}),
            })
        return result
