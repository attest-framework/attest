"""Recorded refund-desk incidents and fixed runs (protocol-tests/incidents/cases.json).

Each recording's applicable contracts are built with the DSL, evaluated through the built
engine in one batch, and every row must get its expected status, in order; hard_fail rows
must also match their explanation. The incidents must fail on exactly the contracts they
broke, and the fixed runs must pass the rest, so a false pass, a wrong contract firing or a
misrouted verdict fails here. The TypeScript SDK checks the same file.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from attest import ExpectChain, Trace, expect, load_fixture, related
from attest.plugin import AttestEngineFixture

_PROTOCOL_TESTS = Path(__file__).resolve().parents[4] / "protocol-tests"
_CASES: dict[str, Any] = json.loads((_PROTOCOL_TESTS / "incidents" / "cases.json").read_text())
_CONTRACTS: dict[str, dict[str, Any]] = {c["id"]: c for c in _CASES["contracts"]}


def _arg(value: Any) -> Any:
    if isinstance(value, dict) and "$related" in value:
        ref = value["$related"]
        return related(ref["resource"], ref["field"], on=ref["on"])
    return value


def _chain(recording: dict[str, Any], contract_ids: list[str]) -> ExpectChain:
    if "trace" in recording:
        trace = Trace.from_dict(json.loads((_PROTOCOL_TESTS / recording["trace"]).read_text()))
        world = None
    else:
        fixture = load_fixture(_PROTOCOL_TESTS / recording["fixture"])
        trace, world = fixture.trace, fixture.world
    chain = expect(trace)
    for contract_id in contract_ids:
        contract = _CONTRACTS[contract_id]
        call = contract["python"]
        target: Any = chain.world(world) if contract.get("effect") else chain
        getattr(target, call["method"])(
            *[_arg(a) for a in call["args"]],
            **{key: _arg(value) for key, value in call["kwargs"].items()},
        )
    return chain


@pytest.mark.parametrize("name", sorted(_CASES["expected"]))
def test_recording_gets_every_expected_contract_status(
    engine: AttestEngineFixture, name: str
) -> None:
    expected = _CASES["expected"][name]
    chain = _chain(_CASES["recordings"][name], [row["contract"] for row in expected])

    rows = engine.inspect(chain).assertion_results

    assert [
        {
            "contract": want["contract"],
            "status": got.status,
            **({"explanation": got.explanation} if got.status == "hard_fail" else {}),
        }
        for want, got in zip(expected, rows, strict=True)
    ] == expected


def test_every_incident_breaks_a_contract_and_every_fixed_run_passes_its_own() -> None:
    for name, rows in _CASES["expected"].items():
        failed = [row["contract"] for row in rows if row["status"] != "pass"]
        if name.endswith("-fixed"):
            # The fixed INC-1077 run makes two valid partial refunds, so only the
            # one-refund-per-order contract (too strict for it) may fail there.
            assert failed in ([], ["cardinality-issue_refund-per-order_id"]), name
        else:
            assert failed, f"{name} is an incident but no contract fails on it"
