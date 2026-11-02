"""``--attest-trials`` against the real engine: trial rows, deterministic IDs,
raised trials and usage reach the report-v2 file."""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from tests.integration.conftest import _engine_binary_path

pytest_plugins = ["pytester"]

_SCHEMA: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[4] / "proto" / "attest" / "v1" / "report-v2.schema.json"
    ).read_text()
)

_TESTS = """
import pytest

from attest import agent, expect

_calls = {"n": 0}


@agent("refund-agent")
def refund_agent(builder, order_id):
    _calls["n"] += 1
    if _calls["n"] == 2:
        raise ConnectionError("model server returned HTTP 500")
    builder.add_tool_call("process_refund", args={"order_id": order_id},
                          result={"refund_id": "RFD-001"})
    builder.set_metadata(cost_usd=0.001)
    return {"message": "Your refund has been processed."}


def test_flaky(attest):
    result = refund_agent(order_id="ORD-1")
    attest.evaluate(expect(result).output_contains("refund").cost_under(0.05))


def test_always_wrong(attest):
    attest.evaluate(expect(refund_agent(order_id="ORD-2")).output_contains("apology"))


def test_assert_after_evaluate(attest):
    result = refund_agent(order_id="ORD-4")
    attest.evaluate(expect(result).output_contains("refund"))
    assert False, "checked after evaluate"


@pytest.mark.attest_budget(max_cost_usd=0.001)
def test_budget_stops(attest):
    attest.evaluate(expect(refund_agent(order_id="ORD-3")).output_contains("refund"))


@pytest.mark.attest_budget(max_calls=1)
def test_bad_marker(attest):
    pass


@pytest.mark.asyncio
async def test_async(attest):
    pass
"""


@pytest.fixture
def scratch(pytester: pytest.Pytester, monkeypatch: pytest.MonkeyPatch) -> pytest.Pytester:
    path = _engine_binary_path()
    if not os.path.exists(path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    for key in ("OPENAI_API_KEY", "ATTEST_JUDGE_API_KEY", "ATTEST_JUDGE_BASE_URL"):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.delenv("ATTEST_SIMULATION", raising=False)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", path)
    monkeypatch.setenv("COLUMNS", "200")
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(pytester.path / ".cache"))
    pytester.makepyfile(test_refund=_TESTS)
    return pytester


@pytest.mark.integration
def test_trials_rows_ids_and_usage_reach_report(scratch: pytest.Pytester) -> None:
    run = scratch.runpytest_subprocess(
        "-p", "no:cacheprovider", "--attest-trials=3", "--attest-report=r.json"
    )

    run.assert_outcomes(passed=2, failed=4)
    run.stdout.fnmatch_lines(
        [
            "*test_refund.py::test_flaky: 2/3 trials passed (0 not evaluated)*",
            "*test_refund.py::test_budget_stops: stopped after 1 of 3 trials: test budget spent*",
            "*unknown argument(s) max_calls*",
            "*--attest-trials supports synchronous tests*attest.simulation.repeat*",
        ]
    )
    report = json.loads((scratch.path / "r.json").read_text())
    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert set(report["summary"]["usage"]) >= {"model_calls", "judge_calls", "budget_refused"}

    tests = {t["name"]: t for t in report["tests"]}
    assert tests["test_flaky"]["trials"] == 3
    assert tests["test_flaky"]["outcome"] == "passed"
    assert tests["test_always_wrong"]["outcome"] == "failed"
    assert tests["test_budget_stops"]["trials"] == 1
    assert tests["test_assert_after_evaluate"]["outcome"] == "failed"
    assert tests["test_assert_after_evaluate"]["trials"] == 3
    assert len(tests["test_assert_after_evaluate"]["assertion_ids"]) == 1
    after = [r for r in report["results"] if r["test_id"].endswith("::test_assert_after_evaluate")]
    assert [(r["trial"], r["status"]) for r in after] == [
        (1, "hard_fail"),
        (2, "hard_fail"),
        (3, "hard_fail"),
    ]
    assert after[0]["explanation"] == (
        "trial 1 raised AssertionError: checked after evaluate\nassert False"
        " (engine status before the error: pass)"
    )

    flaky = [r for r in report["results"] if r["test_id"].endswith("::test_flaky")]
    assert sorted({r["trial"] for r in flaky}) == [1, 2, 3]
    ids_by_trial = {t: sorted(r["assertion_id"] for r in flaky if r["trial"] == t) for t in (1, 3)}
    assert ids_by_trial[1] == ids_by_trial[3]
    raised = [r for r in flaky if r["trial"] == 2]
    assert sorted(r["assertion_id"] for r in raised) == ids_by_trial[1]
    assert {r["status"] for r in raised} == {"hard_fail"}
    assert raised[0]["explanation"] == (
        "trial 2 raised ConnectionError: model server returned HTTP 500"
    )


@pytest.mark.integration
def test_assertion_ids_stable_across_runs(scratch: pytest.Pytester) -> None:
    ids = []
    for name in ("a.json", "b.json"):
        scratch.runpytest_subprocess(
            "-p", "no:cacheprovider", "-k", "always_wrong", f"--attest-report={name}"
        )
        report = json.loads((scratch.path / name).read_text())
        ids.append([r["assertion_id"] for r in report["results"]])

    assert ids[0] == ids[1]
    assert len(ids[0]) == 1
