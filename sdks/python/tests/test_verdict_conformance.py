"""Cross-SDK verdict fixtures (protocol-tests/verdicts): outcome aggregation."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest._proto.types import AssertionResult, Trace
from attest.result import AgentResult

_VERDICTS = Path(__file__).resolve().parents[3] / "protocol-tests" / "verdicts"
_SCHEMA = json.loads((_VERDICTS / "verdict.schema.json").read_text())
_FIXTURES = sorted(_VERDICTS.glob("[0-9]*.json"))


def _load(path: Path) -> dict[str, Any]:
    data: dict[str, Any] = json.loads(path.read_text())
    return data


def _agent_result(fixture: dict[str, Any]) -> AgentResult:
    return AgentResult(
        trace=Trace(trace_id="trc_verdict", output={"message": "x"}),
        assertion_results=[AssertionResult.from_dict(r) for r in fixture["results"]],
    )


def test_verdict_fixtures_present() -> None:
    assert len(_FIXTURES) >= 9


@pytest.mark.parametrize("path", _FIXTURES, ids=lambda p: p.stem)
def test_verdict_fixture_aggregates_per_contract(path: Path) -> None:
    fixture = _load(path)
    jsonschema.validate(fixture, _SCHEMA)
    expected = fixture["expected"]

    result = _agent_result(fixture)

    assert result.outcome() == expected["outcome"]
    assert result.outcome(strict=True) == expected["strictOutcome"]
    assert [r.assertion_id for r in result.failed_assertions] == expected["failedAssertionIds"]
    assert result.not_evaluated_reasons == expected["notEvaluated"]
    assert result.pass_rate == expected["passRate"]
    assert bool(result.soft_failures) == expected["softFailWarning"]
