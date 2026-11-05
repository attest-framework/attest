"""report-v2 (proto/attest/v1/report-v2.schema.json): cross-SDK fixtures, the
report builder and the file `pytest --attest-report` writes."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest._proto.types import AssertionResult, Trace, Usage
from attest.report_v2 import TestRecord, build_report, write_report
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

    report = build_report([failed, skipped], simulated=False, usage=Usage())

    _validate(report)
    assert report["summary"] == {
        "total": 4,
        "passed": 1,
        "soft_fail": 0,
        "hard_fail": 1,
        "not_evaluated": 2,
        "usage": Usage().to_dict(),
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


def _run_phase(item: object, when: str, outcome: str) -> None:
    """Drive the plugin's makereport hookwrapper for one phase of ``item``."""
    from attest import plugin

    report = type(
        "Report",
        (),
        {"when": when, "outcome": outcome, "duration": 0.001, "skipped": False, "longrepr": None},
    )()
    hook = plugin.pytest_runtest_makereport(item, type("Call", (), {"when": when})())  # type: ignore[arg-type]
    next(hook)
    with pytest.raises(StopIteration):
        hook.send(type("Outcome", (), {"get_result": lambda self: report})())


def test_rerun_test_reports_only_the_last_attempt() -> None:
    from attest import plugin

    class _Item:
        stash = pytest.Stash()
        nodeid = "tests/test_a.py::test_flaky"
        name = "test_flaky"
        location = ("tests/test_a.py", 2, "test_flaky")
        path = "tests/test_a.py"
        config = type("Config", (), {"getoption": lambda self, *a, **k: "r.json"})()

    plugin._session_tests.clear()
    plugin._take_test_results()
    item = _Item()
    hard = AssertionResult(assertion_id="first", status="hard_fail", score=0.0, explanation="")
    ok = AssertionResult(assertion_id="second", status="pass", score=1.0, explanation="")

    _run_phase(item, "setup", "passed")
    plugin._record_result(_agent_result(hard))
    _run_phase(item, "call", "failed")
    _run_phase(item, "teardown", "passed")
    _run_phase(item, "setup", "passed")
    plugin._record_result(_agent_result(ok))
    _run_phase(item, "call", "passed")
    _run_phase(item, "teardown", "passed")

    report = build_report(list(plugin._session_tests.values()), simulated=False, usage=Usage())
    plugin._session_tests.clear()
    _validate(report)
    assert report["tests"][0]["outcome"] == "passed"
    assert report["tests"][0]["line"] == 3
    assert [r["assertion_id"] for r in report["results"]] == ["second"]
    assert report["summary"]["hard_fail"] == 0
    assert report["tests"][0]["duration_ms"] == 3  # setup, call, teardown of the last attempt


@pytest.mark.parametrize("report_path", [None, "r.json"])
def test_setup_and_teardown_results_count_in_soft_failure_summary(report_path: str | None) -> None:
    from attest import plugin

    class _Item:
        stash = pytest.Stash()
        nodeid = "tests/test_a.py::test_fixture_evaluates"
        name = "test_fixture_evaluates"
        location = ("tests/test_a.py", 0, "test_fixture_evaluates")
        path = "tests/test_a.py"
        config = type("Config", (), {"getoption": lambda self, *a, **k: report_path})()

    plugin._session_tests.clear()
    plugin._take_test_results()
    item = _Item()
    soft = AssertionResult(assertion_id="s", status="soft_fail", score=0.6, explanation="")

    for when in ("setup", "call", "teardown"):
        plugin._record_result(_agent_result(soft))
        _run_phase(item, when, "passed")

    assert plugin._session_soft_counts.pop(item.nodeid) == 3
    assert plugin._session_outcomes.pop(item.nodeid) == "passed"
    assert plugin._session_results.pop(item.nodeid).soft_failures
    assert plugin._take_test_results() == []
    tests = list(plugin._session_tests.values())
    plugin._session_tests.clear()
    if report_path is not None:
        assert len(tests[0].agent_results) == 3


def _traced_record(*traces: Trace) -> TestRecord:
    return TestRecord(
        test_id="tests/test_a.py::test_refund",
        name="test_refund",
        file="tests/test_a.py",
        line=7,
        agent_results=[
            AgentResult(
                trace=trace,
                assertion_results=[
                    AssertionResult(assertion_id="a1", status="pass", score=1.0, explanation="")
                ],
            )
            for trace in traces
        ],
    )


def test_build_report_traces_off_has_no_traces_or_trace_id() -> None:
    record = _traced_record(Trace(trace_id="trc_1", output={"message": "ok"}))

    report = build_report([record], simulated=False, usage=Usage())

    assert "traces" not in report
    assert all("trace_id" not in row for row in report["results"])


def test_build_report_traces_on_embeds_trace_and_tags_rows_per_schema() -> None:
    record = _traced_record(Trace(trace_id="trc_1", output={"message": "ok"}))

    report = build_report([record], simulated=False, usage=Usage(), include_traces=True)

    assert report["traces"] == {"trc_1": record.agent_results[0].trace.to_dict()}
    assert [row["trace_id"] for row in report["results"]] == ["trc_1"]
    _validate(report)


def test_build_report_traces_on_dedupes_equal_traces_and_splits_colliding_ids() -> None:
    same = Trace(trace_id="trc_1", output={"message": "ok"})
    other = Trace(trace_id="trc_1", output={"message": "different"})
    unnamed = Trace(trace_id="", output={"message": "x"})
    record = _traced_record(same, same, other, unnamed)

    report = build_report([record], simulated=False, usage=Usage(), include_traces=True)

    assert [row["trace_id"] for row in report["results"]] == [
        "trc_1",
        "trc_1",
        "trc_1#2",
        "trace-3",
    ]
    assert report["traces"]["trc_1#2"]["output"] == {"message": "different"}
    assert len(report["traces"]) == 3


@pytest.mark.parametrize(
    ("env", "embedded"), [(None, False), ("0", False), ("1", True)], ids=["unset", "zero", "one"]
)
def test_write_report_embeds_traces_only_when_env_is_1(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, env: str | None, embedded: bool
) -> None:
    if env is None:
        monkeypatch.delenv("ATTEST_REPORT_TRACES", raising=False)
    else:
        monkeypatch.setenv("ATTEST_REPORT_TRACES", env)
    record = _traced_record(Trace(trace_id="trc_1", output={"message": "ok"}))
    path = tmp_path / "r.json"

    write_report(path, [record], simulated=False, usage=Usage(), tests_collected=1)

    report = json.loads(path.read_text())
    assert ("traces" in report) is embedded
    assert all(("trace_id" in row) is embedded for row in report["results"])
    _validate(report)
