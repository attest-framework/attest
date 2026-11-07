"""``sum`` / ``matches`` / ``related`` builders: the engine spec they produce and the build-time
validation of their arguments."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from attest import expect, load_fixture
from attest._proto.types import Trace
from attest.expect import Related, related

_ROOT = Path(__file__).resolve().parents[3]
_TRACE = Trace.from_dict(
    json.loads(
        (_ROOT / "protocol-tests" / "trace-v2" / "otlp" / "inc-1042-refund.trace.json").read_text()
    )
)
_FIXTURE = load_fixture(
    _ROOT / "protocol-tests" / "fixture-v1" / "valid" / "005-world-snapshot.attest.json"
)
_WORLD: dict[str, Any] = {"state": {}}


def _spec(chain: Any) -> dict[str, Any]:
    spec: dict[str, Any] = dict(chain.assertions[-1].spec)
    spec.pop("world")
    return spec


def test_related_builds_the_engine_object() -> None:
    assert related("orders", "amount_cents", on="order_id") == Related(
        "orders", "amount_cents", "order_id"
    )
    assert related("orders", "amount_cents", on="order_id").to_spec() == {
        "resource": "orders",
        "field": "amount_cents",
        "on": "order_id",
    }


@pytest.mark.parametrize("bad", ["", None, 3])
def test_related_non_string_argument_raises(bad: Any) -> None:
    with pytest.raises(TypeError, match="related: on must be a non-empty string"):
        related("orders", "amount_cents", on=bad)


def test_sum_related_bound_produces_the_spec() -> None:
    chain = (
        expect(_TRACE)
        .world(_WORLD)
        .sum("refunds", "amount_cents", at_most=related("orders", "amount_cents", on="order_id"))
    )

    assert _spec(chain) == {
        "check": "sum",
        "resource": "refunds",
        "field": "amount_cents",
        "at_most": {"related": {"resource": "orders", "field": "amount_cents", "on": "order_id"}},
    }


def test_sum_number_bound_with_filter_produces_the_spec() -> None:
    chain = (
        expect(_TRACE).world(_WORLD).sum("refunds", "amount_cents", at_most=15000, status="paid")
    )

    assert _spec(chain) == {
        "check": "sum",
        "resource": "refunds",
        "field": "amount_cents",
        "at_most": 15000,
        "where": {"status": "paid"},
    }


def test_matches_produces_the_spec() -> None:
    chain = (
        expect(_TRACE)
        .world(_WORLD)
        .matches("notifications", "customer_id", related("orders", "customer_id", on="order_id"))
    )

    assert _spec(chain) == {
        "check": "matches",
        "resource": "notifications",
        "field": "customer_id",
        "related": {"resource": "orders", "field": "customer_id", "on": "order_id"},
    }


@pytest.mark.parametrize("bad", ["100", None, True, float("nan"), float("inf"), {"related": {}}])
def test_sum_bad_at_most_raises(bad: Any) -> None:
    with pytest.raises(TypeError, match="sum: at_most must be"):
        expect(_TRACE).world(_WORLD).sum("refunds", "amount_cents", at_most=bad)


def test_sum_missing_at_most_raises() -> None:
    with pytest.raises(TypeError):
        expect(_TRACE).world(_WORLD).sum("refunds", "amount_cents")  # type: ignore[call-arg]


def test_sum_empty_field_raises() -> None:
    with pytest.raises(TypeError, match="sum: field must be a non-empty string"):
        expect(_TRACE).world(_WORLD).sum("refunds", "", at_most=1)


def test_matches_plain_third_argument_raises() -> None:
    with pytest.raises(TypeError, match=r"matches: the third argument must be related\(\.\.\.\)"):
        expect(_TRACE).world(_WORLD).matches("notifications", "customer_id", "orders")  # type: ignore[arg-type]


def test_sum_filter_in_where_and_keyword_raises() -> None:
    with pytest.raises(TypeError, match="filter given both"):
        expect(_TRACE).world(_WORLD).sum(
            "refunds", "amount_cents", at_most=1, where={"status": "a"}, status="b"
        )
