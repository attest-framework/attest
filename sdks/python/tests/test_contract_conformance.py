"""Cross-SDK behavior-contract fixtures (protocol-tests/contracts/dsl.json).

Each case names a DSL call; the Python chain must emit exactly the fixture's
spec. The TypeScript SDK checks the same specs, and the engine evaluates them.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest import expect
from attest._proto.types import TYPE_TRACE, Trace

_ROOT = Path(__file__).resolve().parents[3]
_TESTS = _ROOT / "protocol-tests"
_FIXTURE: dict[str, Any] = json.loads((_TESTS / "contracts" / "dsl.json").read_text())
_TRACE_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "trace-v2.schema.json").read_text()
)


def _trace(name: str) -> Trace:
    return Trace.from_dict(json.loads((_TESTS / _FIXTURE["traces"][name]).read_text()))


@pytest.mark.parametrize("case", _FIXTURE["cases"], ids=lambda c: c["name"])
def test_contract_dsl_emits_fixture_spec(case: dict[str, Any]) -> None:
    call = case["python"]
    chain = expect(_trace("inc-1042-py"))

    getattr(chain, call["method"])(*call["args"], **call["kwargs"])

    [assertion] = chain.assertions
    assert assertion.type == TYPE_TRACE
    assert json.loads(json.dumps(assertion.spec)) == case["spec"]


@pytest.mark.parametrize("name", sorted(_FIXTURE["traces"]))
def test_contract_fixture_trace_matches_trace_v2_schema(name: str) -> None:
    jsonschema.validate(json.loads((_TESTS / _FIXTURE["traces"][name]).read_text()), _TRACE_SCHEMA)


def test_tool_cardinality_without_bounds_raises() -> None:
    with pytest.raises(ValueError, match="at_most, at_least or both"):
        expect(_trace("inc-1042-py")).tool_cardinality("issue_refund", per="order_id")
