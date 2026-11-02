"""Behavior contracts evaluated by the engine on the frozen INC-1042 traces."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from attest import expect
from attest._proto.types import STATUS_HARD_FAIL, STATUS_PASS, Trace
from attest.plugin import AttestEngineFixture

_TESTS = Path(__file__).resolve().parents[4] / "protocol-tests"


def _trace(path: str) -> Trace:
    return Trace.from_dict(json.loads((_TESTS / path).read_text()))


@pytest.mark.integration
@pytest.mark.parametrize(
    "path",
    ["trace-v2/otlp/inc-1042-refund.trace.json", "contracts/traces/inc-1042-ts.trace.json"],
    ids=["py", "ts"],
)
def test_refund_contract_reports_inc_1042_double_refund(
    engine: AttestEngineFixture, path: str
) -> None:
    chain = expect(_trace(path)).tool_cardinality("issue_refund", per="order_id", at_most=1)

    result = engine.inspect(chain)

    [row] = result.assertion_results
    assert row.status == STATUS_HARD_FAIL
    assert row.explanation == "issue_refund called 2× for order_id=O-1042 (max 1)"


@pytest.mark.integration
def test_refund_contract_passes_corrected_trace(engine: AttestEngineFixture) -> None:
    chain = expect(_trace("contracts/traces/inc-1042-corrected.trace.json")).tool_cardinality(
        "issue_refund", per="order_id", at_most=1
    )

    result = engine.inspect(chain)

    assert [r.status for r in result.assertion_results] == [STATUS_PASS]


@pytest.mark.integration
def test_cycle_period_2_fails_on_abab(engine: AttestEngineFixture) -> None:
    chain = expect(_trace("contracts/traces/abab.trace.json")).no_tool_cycles(period=2)

    result = engine.inspect(chain)

    [row] = result.assertion_results
    assert row.status == STATUS_HARD_FAIL
    assert row.explanation.startswith("tool cycle of period 2 repeated 2×")
