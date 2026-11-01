"""report-v2 (proto/attest/v1/report-v2.schema.json): cross-SDK fixtures, the
report builder and the file `pytest --attest-report` writes."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest._proto.types import AssertionResult, Trace
from attest.report_v2 import TestRecord, build_report
from attest.result import AgentResult

pytest_plugins = ["pytester"]

_ROOT = Path(__file__).resolve().parents[3]
_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "report-v2.schema.json").read_text()
)
_VALID = sorted((_ROOT / "protocol-tests" / "report-v2" / "valid").glob("*.json"))
_INVALID = sorted((_ROOT / "protocol-tests" / "report-v2" / "invalid").glob("*.json"))


def _validate(doc: object) -> None:
    jsonschema.validate(doc, _SCHEMA, format_checker=jsonschema.FormatChecker())


def test_report_v2_fixtures_present() -> None:
    assert _VALID
    assert _INVALID


@pytest.mark.parametrize("path", _VALID, ids=lambda p: p.stem)
def test_report_v2_valid_fixture_matches_schema(path: Path) -> None:
    _validate(json.loads(path.read_text()))


@pytest.mark.parametrize("path", _INVALID, ids=lambda p: p.stem)
def test_report_v2_invalid_fixture_is_rejected(path: Path) -> None:
    with pytest.raises(jsonschema.ValidationError):
        _validate(json.loads(path.read_text()))


def _agent_result(*results: AssertionResult) -> AgentResult:
    return AgentResult(
        trace=Trace(trace_id="trc_1", output={"message": "ok"}),
        assertion_results=list(results),
        total_cost=0.001,
        total_duration_ms=3,
    )


def test_build_report_tags_rows_with_test_location_and_matches_schema() -> None:
    failed = TestRecord(
        test_id="tests/test_a.py::test_refund",
        name="test_refund",
        file="tests/test_a.py",
        line=7,
        outcome="failed",
        agent_results=[
            _agent_result(
                AssertionResult(assertion_id="a1", status="pass", score=1.0, explanation=""),
                AssertionResult(
                    assertion_id="a2",
                    status="hard_fail",
                    score=0.0,
                    explanation="missing",
                    layer=4,
                    expected='contains "store credit"',
                    actual="Your refund",
                    failure_class="broken_code",
                ),
            )
        ],
    )
    skipped = TestRecord(
        test_id="tests/test_a.py::test_tone",
        name="test_tone",
        file="tests/test_a.py",
        line=20,
        outcome="skipped",
        agent_results=[
            _agent_result(
                AssertionResult(
                    assertion_id="a3",
                    status="not_evaluated",
                    score=0.0,
                    explanation="",
                    reason="provider_not_configured",
                ),
                AssertionResult(
                    assertion_id="a4",
                    status="not_evaluated",
                    score=0.0,
                    explanation="",
                    reason="provider_not_configured",
                ),
            )
        ],
    )

    report = build_report([failed, skipped], simulated=False)

    _validate(report)
    assert report["summary"] == {
        "total": 4,
        "passed": 1,
        "soft_fail": 0,
        "hard_fail": 1,
        "not_evaluated": 2,
    }
    hard = next(r for r in report["results"] if r["status"] == "hard_fail")
    assert (hard["test_id"], hard["file"], hard["line"]) == (
        "tests/test_a.py::test_refund",
        "tests/test_a.py",
        7,
    )
    assert report["tests"][1]["not_evaluated_reasons"] == ["provider_not_configured"]
    assert report["tests"][0]["assertion_ids"] == ["a1", "a2"]


@pytest.mark.parametrize(("line", "outcome"), [(0, "passed"), (1, "error")])
def test_test_record_invalid_location_or_outcome_raises(line: int, outcome: str) -> None:
    with pytest.raises(ValueError):
        TestRecord(test_id="t", name="t", file="t.py", line=line, outcome=outcome)


def test_attest_report_option_writes_every_test_with_outcome_and_line(
    pytester: pytest.Pytester,
) -> None:
    pytester.makepyfile(
        test_sample="""
        import pytest

        def test_ok():
            pass

        def test_bad():
            assert False

        def test_skipped():
            pytest.skip("not today")

        @pytest.fixture
        def broken_teardown():
            yield
            raise RuntimeError("teardown")

        def test_teardown_error(broken_teardown):
            pass
        """
    )

    result = pytester.runpytest("--attest-report=out/r.json")

    result.assert_outcomes(passed=2, failed=1, skipped=1, errors=1)
    report = json.loads((pytester.path / "out" / "r.json").read_text())
    _validate(report)
    by_name = {t["name"]: t for t in report["tests"]}
    assert {n: (t["outcome"], t["line"]) for n, t in by_name.items()} == {
        "test_ok": ("passed", 3),
        "test_bad": ("failed", 6),
        "test_skipped": ("skipped", 9),
        "test_teardown_error": ("failed", 17),
    }
    assert {t["file"] for t in report["tests"]} == {"test_sample.py"}


def test_attest_report_not_written_without_option(pytester: pytest.Pytester) -> None:
    pytester.makepyfile(test_sample="def test_ok():\n    pass\n")

    pytester.runpytest().assert_outcomes(passed=1)

    assert not list(pytester.path.rglob("*.json"))
