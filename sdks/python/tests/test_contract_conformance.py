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
from attest._proto.types import TYPE_EFFECT, TYPE_TRACE, Trace

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
    # Compare JSON text, not Python values: True == 1 and 1 == 1.0 in Python.
    assert json.dumps(assertion.spec, sort_keys=True) == json.dumps(case["spec"], sort_keys=True)


@pytest.mark.parametrize("name", sorted(_FIXTURE["traces"]))
def test_contract_fixture_trace_matches_trace_v2_schema(name: str) -> None:
    jsonschema.validate(json.loads((_TESTS / _FIXTURE["traces"][name]).read_text()), _TRACE_SCHEMA)


def test_tool_cardinality_without_bounds_raises() -> None:
    with pytest.raises(ValueError, match="at_most, at_least or both"):
        expect(_trace("inc-1042-py")).tool_cardinality("issue_refund", per="order_id")


_EFFECT_FIXTURE: dict[str, Any] = json.loads((_TESTS / "contracts" / "effect-dsl.json").read_text())


@pytest.mark.parametrize("case", _EFFECT_FIXTURE["cases"], ids=lambda c: c["name"])
def test_effect_dsl_emits_fixture_spec(case: dict[str, Any]) -> None:
    call = case["python"]
    chain = expect(_trace("inc-1042-py"))
    snapshot = None if case["world"] is None else _EFFECT_FIXTURE["worlds"][case["world"]]

    getattr(chain.world(snapshot), call["method"])(*call["args"], **call["kwargs"])

    [assertion] = chain.assertions
    assert assertion.type == TYPE_EFFECT
    assert json.dumps(assertion.spec, sort_keys=True) == json.dumps(
        {**case["spec"], "world": snapshot}, sort_keys=True
    )


def test_count_without_bound_raises() -> None:
    with pytest.raises(ValueError, match="needs equals, at_most or at_least"):
        expect(_trace("inc-1042-py")).world(None).count("refunds", order_id="O-1042")


def test_count_equals_with_another_bound_raises() -> None:
    with pytest.raises(ValueError, match="equals excludes at_most and at_least"):
        expect(_trace("inc-1042-py")).world(None).count("refunds", equals=1, at_most=2)


def test_unique_without_fields_raises() -> None:
    with pytest.raises(ValueError, match="at least one field"):
        expect(_trace("inc-1042-py")).world(None).unique("refunds", by=[])


def test_effect_where_dict_and_keywords_merge() -> None:
    chain = expect(_trace("inc-1042-py"))

    chain.world(None).count("refunds", equals=1, where={"key": "k"}, order_id="O-1042")

    assert chain.assertions[0].spec["where"] == {"key": "k", "order_id": "O-1042"}


def test_effect_filter_given_twice_raises() -> None:
    with pytest.raises(TypeError, match="both in where and as a keyword: order_id"):
        expect(_trace("inc-1042-py")).world(None).count(
            "refunds", equals=1, where={"order_id": "O-1"}, order_id="O-1042"
        )


def test_effect_resource_is_positional_only() -> None:
    with pytest.raises(TypeError):
        expect(_trace("inc-1042-py")).world(None).count(resource="refunds", equals=1)  # type: ignore[call-arg]


def test_world_checks_over_the_request_limit_raise_before_sending() -> None:
    big = {"world_version": 1, "state": {"refunds": [{"note": "é" * (2 * 1024 * 1024)}]}}
    chain = expect(_trace("inc-1042-py"))
    world = chain.world(big)

    world.count("refunds", equals=0)
    world.count("refunds", equals=0)
    with pytest.raises(ValueError, match="over the 9437184-byte limit"):
        world.count("refunds", equals=0)
