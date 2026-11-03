"""``attest-engine report`` over reports the Python SDK wrote: the JUnit parses with
junitparser, the summary carries the replay line and the rows locate failures."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from typing import Any

import pytest
from junitparser import Failure, JUnitXml, Skipped

from attest._proto.types import Usage
from attest.replay import ReplaySummary
from attest.report_v2 import TestRecord, build_report
from tests.integration.conftest import _engine_binary_path

_MISS = (
    "replay miss: tools changed (hash 1111111→2222222); "
    "re-record with `attest capture --refresh inc-1042`"
)


def _replay(mode: str, *, replayed: int, misses: int) -> ReplaySummary:
    records: list[dict[str, Any]] = [
        {
            "seq": 0,
            "provider": "openai",
            "api": "chat_completions",
            "outcome": "miss",
            "message": _MISS,
        }
    ]
    return ReplaySummary.from_dict(
        {
            "fixture": "inc-1042",
            "mode": mode,
            "replayed": replayed,
            "live": 0,
            "misses": misses,
            "unused": 0,
            "live_cost_usd": 0.0,
            "unpriced_live_calls": 0,
            "records": records if misses else [],
        }
    )


def _engine() -> str:
    path = _engine_binary_path()
    if not Path(path).exists():
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    return path


@pytest.fixture
def report_path(tmp_path: Path) -> Path:
    passed = TestRecord(test_id="tests/t.py::test_ok", name="test_ok", file="tests/t.py", line=5)
    passed.replay = [_replay("strict", replayed=2, misses=0)]
    missed = TestRecord(
        test_id="tests/t.py::test_strict",
        name="test_strict",
        file="tests/t.py",
        line=20,
        outcome="failed",
    )
    missed.replay = [_replay("strict", replayed=0, misses=1)]
    skipped = TestRecord(
        test_id="tests/t.py::test_auto",
        name="test_auto",
        file="tests/t.py",
        line=30,
        outcome="skipped",
    )
    skipped.replay = [_replay("auto", replayed=0, misses=1)]
    path = tmp_path / "report.json"
    path.write_text(
        json.dumps(build_report([passed, missed, skipped], simulated=False, usage=Usage()))
    )
    return path


@pytest.mark.integration
def test_report_junit_parses_with_junitparser(report_path: Path, tmp_path: Path) -> None:
    out = tmp_path / "junit.xml"

    subprocess.run(
        [_engine(), "report", "junit", "--report", str(report_path), "--out", str(out)], check=True
    )

    suites = list(JUnitXml.fromfile(str(out)))
    assert [(s.name, s.tests, s.failures, s.skipped) for s in suites] == [("tests/t.py", 3, 1, 1)]
    cases = {case.name: case for case in suites[0]}
    assert [type(r) for r in cases["test_strict"].result] == [Failure]
    assert cases["test_strict"].result[0].message == _MISS
    assert [type(r) for r in cases["test_auto"].result] == [Skipped]
    assert cases["test_ok"].result == []


@pytest.mark.integration
def test_report_summary_has_replay_line_and_miss_rows(report_path: Path) -> None:
    result = subprocess.run(
        [_engine(), "report", "summary", "--report", str(report_path)],
        check=True,
        capture_output=True,
        text=True,
    )

    lines = result.stdout.splitlines()
    assert lines[0] == "### attest: 1 passed · 1 failed · 1 not evaluated"
    assert "2 replayed · 0 live model calls · $0.00 · 2 replay misses" in lines
    assert any(line.startswith("| `tests/t.py:20` |") and "replay_miss" in line for line in lines)
    assert any(line.startswith("| `tests/t.py:30` |") for line in lines)


@pytest.mark.integration
def test_report_rows_json_locates_each_finding(report_path: Path) -> None:
    result = subprocess.run(
        [_engine(), "report", "rows", "--report", str(report_path), "--json"],
        check=True,
        capture_output=True,
        text=True,
    )

    rows = json.loads(result.stdout)
    assert [(r["file"], r["line"], r["status"], r["reason"]) for r in rows] == [
        ("tests/t.py", 20, "hard_fail", "replay_miss"),
        ("tests/t.py", 30, "not_evaluated", "replay_miss"),
    ]


@pytest.mark.integration
def test_report_summary_unreadable_report_exits_2(tmp_path: Path) -> None:
    result = subprocess.run(
        [_engine(), "report", "summary", "--report", str(tmp_path / "nope.json")],
        capture_output=True,
        text=True,
    )

    assert result.returncode == 2
    assert "nope.json" in result.stderr
