"""Anthropic trace capture adapter."""

from __future__ import annotations

from typing import Any

from attest.adapters._base import BaseProviderAdapter


class AnthropicAdapter(BaseProviderAdapter):
    """Captures Anthropic Messages API calls into Attest traces."""

    def _extract_completion(self, response: Any) -> str:
        parts: list[str] = []
        for block in response.content:
            if block.type == "text":
                parts.append(block.text)
        return "\n".join(parts)

    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        return getattr(response, "model", None)

    def _extract_total_tokens(self, response: Any) -> int | None:
        if hasattr(response, "usage"):
            return response.usage.input_tokens + response.usage.output_tokens  # type: ignore[no-any-return]
        return None

    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        calls: list[dict[str, Any]] = []
        for block in response.content:
            if block.type == "tool_use":
                calls.append(
                    {
                        "name": block.name,
                        "args": block.input if isinstance(block.input, dict) else {},
                    }
                )
        return calls
