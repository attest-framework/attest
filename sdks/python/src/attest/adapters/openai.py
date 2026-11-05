"""OpenAI trace capture adapter."""

from __future__ import annotations

import json
from typing import Any

from attest.adapters._base import BaseProviderAdapter


def _is_responses(response: Any) -> bool:
    """A Responses API object (``output`` items) as opposed to a ChatCompletion (``choices``)."""
    return getattr(response, "object", None) == "response"


def _parse_arguments(raw: str) -> dict[str, Any]:
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return {"raw_arguments": raw}
    return parsed if isinstance(parsed, dict) else {"raw_arguments": raw}


class OpenAIAdapter(BaseProviderAdapter):
    """Captures OpenAI Chat Completions and Responses API calls into Attest traces.

    A model refusal is the response's message: it becomes the completion and the
    trace output, and is also recorded as ``refusal`` on the llm_call step.
    """

    def _responses_parts(self, response: Any, part_type: str, field: str) -> list[str]:
        parts: list[str] = []
        for item in response.output:
            if item.type != "message":
                continue
            parts += [getattr(p, field) for p in item.content if p.type == part_type]
        return parts

    def _extract_completion(self, response: Any) -> str:
        if _is_responses(response):
            text = "".join(self._responses_parts(response, "output_text", "text"))
        else:
            text = response.choices[0].message.content or ""
        return text or self._extract_refusal(response) or ""

    def _extract_refusal(self, response: Any) -> str | None:
        if _is_responses(response):
            return "".join(self._responses_parts(response, "refusal", "refusal")) or None
        # ``refusal`` does not exist on messages from openai < 1.40.
        return getattr(response.choices[0].message, "refusal", None) or None

    def _extract_model(self, response: Any, **metadata: Any) -> str | None:
        return getattr(response, "model", None)

    def _extract_total_tokens(self, response: Any) -> int | None:
        if hasattr(response, "usage") and response.usage:
            return response.usage.total_tokens  # type: ignore[no-any-return]
        return None

    def _extract_token_split(self, response: Any) -> tuple[int | None, int | None]:
        usage = getattr(response, "usage", None)
        if not usage:
            return None, None
        if _is_responses(response):
            return usage.input_tokens, usage.output_tokens
        return usage.prompt_tokens, usage.completion_tokens

    def _extract_tool_calls(self, response: Any) -> list[dict[str, Any]]:
        if _is_responses(response):
            return [
                {"name": item.name, "args": _parse_arguments(item.arguments)}
                for item in response.output
                if item.type == "function_call"
            ]
        message = response.choices[0].message
        if not hasattr(message, "tool_calls") or not message.tool_calls:
            return []
        return [
            {"name": tc.function.name, "args": _parse_arguments(tc.function.arguments)}
            for tc in message.tool_calls
        ]

    def _build_output(
        self,
        response: Any,
        completion_text: str,
        **metadata: Any,
    ) -> dict[str, Any]:
        return {
            "message": completion_text,
            "structured": metadata.get("structured_output", {}),
        }
