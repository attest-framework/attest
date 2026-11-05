"""``--attest-report`` under pytest-xdist: the controller writes one report with every
worker's rows and the collected-test count; a serial run reports the same counts."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from typing import Any

import pytest

from attest._proto.types import STATUS_HARD_FAIL, STATUS_PASS, Usage
from attest.report_v2 import merge_reports

pytest_plugins = ["pytester"]

_ENGINE = Path(__file__).resolve().parents[3] / "bin" / "attest-engine"

_FAILING_TESTS = """
from attest import expect
from attest.trace import TraceBuilder
from attest.result import AgentResult

def _wrong_output_chain(label):
    trace = TraceBuilder("agent").set_output(message="refund issued").build()
    return expect(AgentResult(trace=trace)).output_contains(label)

def test_one(attest):
    attest.evaluate(_wrong_output_chain("missing-1"))

def test_two(attest):
    attest.evaluate(_wrong_output_chain("missing-2"))

def test_three(attest):
    attest.evaluate(_wrong_output_chain("missing-3"))

def test_four(attest):
    attest.evaluate(_wrong_output_chain("missing-4"))
"""

_POLICY = "layers:\n  4:\n    max_hard_fails: 0\n"


@pytest.fixture
def engine(monkeypatch: pytest.MonkeyPatch) -> Path:
    if not _ENGINE.is_file():
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(_ENGINE))
    return _ENGINE


def _policy_exit_code(report: Path, policy: Path) -> int:
    proc = subprocess.run(
        [str(_ENGINE), "policy", "evaluate", "--report", str(report), "--policy", str(policy)],
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode


def _counts(report: dict[str, Any]) -> dict[str, int]:
    summary = report["summary"]
    return {
        **{k: summary[k] for k in ("total", "passed", "soft_fail", "hard_fail", "not_evaluated")},
        "tests": len(report["tests"]),
        "tests_collected": report["tests_collected"],
    }


def test_xdist_report_four_failing_tests_has_four_hard_fails_and_policy_exits_1(
    pytester: pytest.Pytester, engine: Path
) -> None:
    pytester.makepyfile(test_sample=_FAILING_TESTS)
    policy = pytester.makefile(".yaml", policy=_POLICY)

    result = pytester.runpytest("-n", "4", "--attest-report=r.json")

    result.assert_outcomes(failed=4)
    report = json.loads((pytester.path / "r.json").read_text())
    assert _counts(report) == {
        "total": 4,
        "passed": 0,
        "soft_fail": 0,
        "hard_fail": 4,
        "not_evaluated": 0,
        "tests": 4,
        "tests_collected": 4,
    }
    assert sorted(t["name"] for t in report["tests"]) == [
        "test_four",
        "test_one",
        "test_three",
        "test_two",
    ]
    assert _policy_exit_code(pytester.path / "r.json", policy) == 1


def test_serial_report_counts_equal_xdist_report_counts(
    pytester: pytest.Pytester, engine: Path
) -> None:
    pytester.makepyfile(test_sample=_FAILING_TESTS)

    pytester.runpytest("--attest-report=serial.json").assert_outcomes(failed=4)
    pytester.runpytest("-n", "4", "--attest-report=parallel.json").assert_outcomes(failed=4)

    serial = json.loads((pytester.path / "serial.json").read_text())
    parallel = json.loads((pytester.path / "parallel.json").read_text())
    assert _counts(serial) == _counts(parallel)
    assert sorted(r["assertion_id"] for r in serial["results"]) == sorted(
        r["assertion_id"] for r in parallel["results"]
    )


def test_serial_report_tests_collected_counts_selected_tests_only(
    pytester: pytest.Pytester,
) -> None:
    pytester.makepyfile(test_sample="def test_a():\n    pass\n\ndef test_b():\n    pass\n")

    pytester.runpytest("-k", "test_a", "--attest-report=r.json").assert_outcomes(passed=1)

    report = json.loads((pytester.path / "r.json").read_text())
    assert report["tests_collected"] == 1
    assert len(report["tests"]) == 1


def test_xdist_report_worker_crash_writes_no_report(pytester: pytest.Pytester) -> None:
    pytester.makepyfile(
        test_sample="import os\n\ndef test_crash():\n    os._exit(1)\n\ndef test_ok():\n    pass\n"
    )

    stale = pytester.path / "r.json"
    stale.write_text('{"report_version": 2, "tests": []}')  # an earlier run's report

    result = pytester.runpytest("-n", "2", "--attest-report=r.json")

    assert result.ret == pytest.ExitCode.INTERNAL_ERROR
    assert not stale.exists()
    result.stdout.fnmatch_lines(
        ["*ERROR: --attest-report not written*ended without sending their report*"]
    )


def _worker_doc(*, trace_id: str, trace_value: str, status: str, test_id: str) -> dict[str, Any]:
    return {
        "report_version": 2,
        "timestamp": "2026-01-01T00:00:00Z",
        "simulated": False,
        "summary": {"total": 1, "passed": 0, "soft_fail": 0, "hard_fail": 0, "not_evaluated": 0},
        "results": [
            {
                "assertion_id": "a",
                "status": status,
                "test_id": test_id,
                "file": "t.py",
                "line": 1,
                "trace_id": trace_id,
            }
        ],
        "tests": [{"test_id": test_id, "name": "t", "file": "t.py", "line": 1}],
        "total_cost": 0.5,
        "total_duration_ms": 10,
        "traces": {trace_id: {"output": {"message": trace_value}}},
    }


def test_merge_reports_distinct_traces_sharing_an_id_are_renumbered_with_their_rows() -> None:
    first = _worker_doc(trace_id="trc", trace_value="x", status=STATUS_PASS, test_id="t.py::a")
    second = _worker_doc(
        trace_id="trc", trace_value="y", status=STATUS_HARD_FAIL, test_id="t.py::b"
    )

    merged = merge_reports([first, second], tests_collected=2, usage=Usage(model_calls=3))

    assert merged["traces"] == {
        "trc": {"output": {"message": "x"}},
        "trc#2": {"output": {"message": "y"}},
    }
    assert [r["trace_id"] for r in merged["results"]] == ["trc", "trc#2"]
    assert merged["summary"]["total"] == 2
    assert merged["summary"]["hard_fail"] == 1
    assert merged["summary"]["usage"]["model_calls"] == 3
    assert merged["total_cost"] == 1.0
    assert merged["total_duration_ms"] == 20
    assert merged["tests_collected"] == 2


def test_merge_reports_equal_traces_share_one_entry() -> None:
    docs = [
        _worker_doc(trace_id="trc", trace_value="x", status=STATUS_PASS, test_id=f"t.py::{n}")
        for n in "ab"
    ]

    merged = merge_reports(docs, tests_collected=2, usage=Usage())

    assert list(merged["traces"]) == ["trc"]
    assert [r["trace_id"] for r in merged["results"]] == ["trc", "trc"]


def test_merge_reports_without_documents_raises() -> None:
    with pytest.raises(ValueError, match="at least one"):
        merge_reports([], tests_collected=0, usage=Usage())
