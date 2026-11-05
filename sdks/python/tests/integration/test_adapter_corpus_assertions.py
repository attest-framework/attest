"""Corpus traces through the real engine: tool arguments are objects, refusals are output."""

from __future__ import annotations

from typing import Any

import pytest

from attest._proto.types import Trace
from attest.expect import expect
from attest.plugin import AttestEngineFixture
from tests.adapter_conformance import case_id, load_cases, produce

_CASES = {case_id(c): c for c in load_cases()}
_ORDER_SCHEMA: dict[str, Any] = {
    "type": "object",
    "properties": {"order_id": {"type": "string"}},
    "required": ["order_id"],
    "additionalProperties": False,
}


def _trace(name: str) -> Trace:
    return produce(_CASES[name])


@pytest.mark.parametrize(
    "name",
    ["openai/chat_tool_call", "openai/responses_tool_call", "langchain/tool_loop"],
)
def test_tool_args_match_schema_passes_on_corpus_trace(
    engine: AttestEngineFixture, name: str
) -> None:
    chain = expect(_trace(name)).tool_args_match_schema("lookup_order", _ORDER_SCHEMA)

    result = engine.inspect(chain)

    assert result.passed, [r.explanation for r in result.assertion_results]


@pytest.mark.parametrize("name", ["openai/chat_refusal", "openai/responses_refusal"])
def test_refusal_text_fails_output_not_contains(engine: AttestEngineFixture, name: str) -> None:
    chain = expect(_trace(name)).output_not_contains("can't")

    result = engine.inspect(chain)

    assert not result.passed
