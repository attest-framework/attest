"""A test passes only when something was evaluated: unevaluated and empty ``expect()`` chains
fail, identical assertions get distinct ids, and the report carries id scheme 2 and the
batch-level ``budget_exceeded`` flag. Runs real pytest sessions against the built engine."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest._proto.types import AssertionResult, EvaluateBatchResult, Usage
from attest.assertion_id import compute_assertion_id
from attest.expect import expect
from attest.report_v2 import TestRecord, build_report, merge_reports
from attest.result import AgentResult
from attest.trace import TraceBuilder

pytest_plugins = ["pytester"]

_ROOT = Path(__file__).resolve().parents[3]
_ENGINE = _ROOT / "bin" / "attest-engine"
_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "report-v2.schema.json").read_text()
)

_PRELUDE = """
from attest import expect
from attest.result import AgentResult
from attest.trace import TraceBuilder

def _result(message="ok", cost=None):
    builder = TraceBuilder("agent").set_output(message=message)
    if cost is not None:
        builder.set_metadata(cost_usd=cost)
    return AgentResult(trace=builder.build())
"""

_CHAIN_TESTS = (
    _PRELUDE
    + """
def test_unevaluated_chain(attest):
    expect(_result()).output_contains("x")

def test_two_unevaluated_chains(attest):
    expect(_result()).output_contains("x")
    expect(_result()).output_contains("y")

def test_unevaluated_empty_chain(attest):
    expect(_result())

def test_evaluated_empty_chain(attest):
    attest.evaluate(expect(_result()))

def test_evaluated_chain(attest):
    attest.evaluate(expect(_result()).output_contains("ok"))

def test_failure_before_evaluation_is_not_masked(attest):
    expect(_result()).output_contains("x")
    assert False, "the test's own failure"

def test_one_evaluated_one_forgotten(attest):
    attest.evaluate(expect(_result()).output_contains("ok"))
    expect(_result()).output_contains("forgotten")

def test_identical_assertions(attest):
    attest.evaluate(expect(_result()).output_contains("ok").output_contains("ok"))
"""
)


@pytest.fixture
def scratch(pytester: pytest.Pytester, monkeypatch: pytest.MonkeyPatch) -> pytest.Pytester:
    if not _ENGINE.is_file():
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(_ENGINE))
    monkeypatch.setenv("ATTEST_ENGINE_NO_DOWNLOAD", "1")
    monkeypatch.delenv("ATTEST_SIMULATION", raising=False)
    monkeypatch.delenv("ATTEST_MAX_SOFT_FAILURES", raising=False)
    monkeypatch.setenv("COLUMNS", "200")
    return pytester


def _report(pytester: pytest.Pytester) -> dict[str, Any]:
    document: dict[str, Any] = json.loads((pytester.path / "r.json").read_text())
    jsonschema.validate(document, _SCHEMA, format_checker=jsonschema.FormatChecker())
    return document


def _outcomes(report: dict[str, Any]) -> dict[str, str]:
    return {t["name"]: t["outcome"] for t in report["tests"]}


def test_chain_teardown_fails_unevaluated_and_empty_chains(scratch: pytest.Pytester) -> None:
    scratch.makepyfile(test_chains=_CHAIN_TESTS)

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    report = _report(scratch)
    assert _outcomes(report) == {
        "test_unevaluated_chain": "failed",
        "test_two_unevaluated_chains": "failed",
        "test_unevaluated_empty_chain": "failed",
        "test_evaluated_empty_chain": "failed",
        "test_evaluated_chain": "passed",
        "test_failure_before_evaluation_is_not_masked": "failed",
        "test_one_evaluated_one_forgotten": "failed",
        "test_identical_assertions": "passed",
    }
    result.assert_outcomes(passed=2, failed=6)
    output = result.stdout.str()
    result.stdout.fnmatch_lines(
        [
            "FAILED *::test_unevaluated_chain - Failed: 1 expect() chain built but never evaluated",
            "FAILED *::test_two_unevaluated_chains - Failed: 2 expect() chains built but never "
            "evaluated",
            "FAILED *::test_unevaluated_empty_chain - Failed: 1 expect() chain built but never "
            "evaluated",
            "FAILED *::test_one_evaluated_one_forgotten - Failed: 1 expect() chain built but "
            "never evaluated",
        ]
    )
    assert "expect() chain has no assertions" in output
    assert "the test's own failure" in output


@pytest.mark.parametrize("asynchronous", [False, True], ids=["sync", "async"])
def test_appending_assertions_requires_another_evaluation(
    scratch: pytest.Pytester, asynchronous: bool
) -> None:
    prefix = "@pytest.mark.asyncio\nasync " if asynchronous else ""
    evaluate = "await attest.evaluate_async" if asynchronous else "attest.evaluate"
    scratch.makepyfile(
        test_append=_PRELUDE
        + "\nimport pytest\n\n"
        + f"""
{prefix}def test_append_without_evaluation(attest):
    chain = expect(_result()).output_contains("ok")
    {evaluate}(chain)
    chain.output_contains("missing")

{prefix}def test_append_and_evaluate_again(attest):
    chain = expect(_result()).output_contains("ok")
    {evaluate}(chain)
    chain.output_not_contains("missing")
    {evaluate}(chain)

{prefix}def test_world_append_without_evaluation(attest):
    chain = expect(_result()).output_contains("ok")
    {evaluate}(chain)
    chain.world(None).count("refunds", equals=1)

def test_next_test_is_clean():
    pass
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    result.assert_outcomes(passed=2, failed=2)
    assert _outcomes(_report(scratch)) == {
        "test_append_without_evaluation": "failed",
        "test_append_and_evaluate_again": "passed",
        "test_world_append_without_evaluation": "failed",
        "test_next_test_is_clean": "passed",
    }
    assert result.stdout.str().count("Failed: 1 expect() chain built but never evaluated") == 2


def test_function_fixture_chains_belong_to_the_consuming_test(scratch: pytest.Pytester) -> None:
    scratch.makepyfile(
        test_fixtures=_PRELUDE
        + """
import pytest

@pytest.fixture
def chain():
    return expect(_result()).output_contains("ok")

@pytest.fixture
def evaluated_in_setup(attest):
    chain = expect(_result()).output_contains("ok")
    attest.evaluate(chain)
    return chain

def test_forgotten_with_attest(chain, attest):
    assert chain.trace.output["message"] == "ok"

def test_forgotten_without_attest(chain):
    assert chain.trace.output["message"] == "ok"

def test_evaluate_in_call(chain, attest):
    attest.evaluate(chain)

def test_setup_evaluation(evaluated_in_setup):
    assert evaluated_in_setup.evaluated

@pytest.mark.parametrize("index", [0, 1])
def test_parametrized_fixture(chain, index):
    assert index >= 0

def test_next_test_is_clean():
    pass
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    result.assert_outcomes(passed=3, failed=4)
    report = _report(scratch)
    outcomes = _outcomes(report)
    assert outcomes == {
        "test_forgotten_with_attest": "failed",
        "test_forgotten_without_attest": "failed",
        "test_evaluate_in_call": "passed",
        "test_setup_evaluation": "passed",
        "test_parametrized_fixture[0]": "failed",
        "test_parametrized_fixture[1]": "failed",
        "test_next_test_is_clean": "passed",
    }
    assert [row["scope_id"] for row in report["results"]] == [
        "test_fixtures.py::test_evaluate_in_call",
        "test_fixtures.py::test_setup_evaluation",
    ]
    ids_by_test = {test["name"]: test["assertion_ids"] for test in report["tests"]}
    assert ids_by_test["test_evaluate_in_call"] == [report["results"][0]["assertion_id"]]
    assert ids_by_test["test_setup_evaluation"] == [report["results"][1]["assertion_id"]]
    assert ids_by_test["test_next_test_is_clean"] == []
    assert result.stdout.str().count("Failed: 1 expect() chain built but never evaluated") == 4


@pytest.mark.parametrize("scope", ["module", "session"])
def test_shared_fixture_chains_belong_to_the_first_setup_test(
    scratch: pytest.Pytester, scope: str
) -> None:
    scratch.makepyfile(
        test_shared=_PRELUDE
        + f"""
import pytest

@pytest.fixture(scope="{scope}")
def shared_chain():
    return expect(_result()).output_contains("missing")

def test_first_consumer(shared_chain):
    assert not shared_chain.evaluated

def test_cached_consumer(shared_chain):
    assert not shared_chain.evaluated

def test_next_test_is_clean():
    pass
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    result.assert_outcomes(passed=2, failed=1)
    assert _outcomes(_report(scratch)) == {
        "test_first_consumer": "failed",
        "test_cached_consumer": "passed",
        "test_next_test_is_clean": "passed",
    }


def test_fixture_teardown_chains_are_checked_without_leaking(scratch: pytest.Pytester) -> None:
    scratch.makepyfile(
        test_teardown=_PRELUDE
        + """
import pytest

@pytest.fixture
def teardown_chain():
    yield
    expect(_result()).output_contains("missing")

@pytest.fixture
def broken_setup():
    expect(_result()).output_contains("missing")
    raise RuntimeError("fixture setup failed")

def test_teardown(teardown_chain):
    pass

def test_broken_setup(broken_setup):
    pass

def test_next_test_is_clean():
    pass
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    result.assert_outcomes(passed=2, errors=2)
    assert _outcomes(_report(scratch)) == {
        "test_teardown": "failed",
        "test_broken_setup": "failed",
        "test_next_test_is_clean": "passed",
    }
    assert result.stdout.str().count("Failed: 1 expect() chain built but never evaluated") == 1


@pytest.mark.parametrize("phase", ["setup", "call"])
@pytest.mark.parametrize("uses_attest", [False, True], ids=["ledger", "run"])
@pytest.mark.parametrize("interrupt", ["pytest.exit('inner aborted')", "raise KeyboardInterrupt"])
def test_aborted_nested_sessions_release_chain_ownership(
    scratch: pytest.Pytester, phase: str, uses_attest: bool, interrupt: str
) -> None:
    dependency = "attest" if uses_attest else ""
    inner = "import pytest\n\n"
    if phase == "setup":
        inner += (
            f"@pytest.fixture\ndef abort({dependency}):\n    {interrupt}\n\n"
            "def test_inner(abort):\n    pass\n"
        )
    else:
        inner += f"def test_inner({dependency}):\n    {interrupt}\n"
    scratch.makeconftest("pytest_plugins = ['pytester']")
    scratch.makepyfile(
        test_outer=_PRELUDE
        + f"""
import pytest
from attest import trials

def test_nested_abort_does_not_disable_guard(pytester):
    ledger_depth = len(trials._ledgers)
    pytester.makepyfile(test_inner={inner!r})
    try:
        result = pytester.runpytest_inprocess("-p", "no:cacheprovider")
    except KeyboardInterrupt:
        pass  # pytester's in-process helper re-raises KeyboardInterrupt on some versions.
    else:
        assert result.ret == pytest.ExitCode.INTERRUPTED
    assert trials.current_run() is None
    assert len(trials._ledgers) == ledger_depth
    expect(_result()).output_contains("missing")

def test_next_test_is_clean():
    pass
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    result.assert_outcomes(passed=1, failed=1)
    assert _outcomes(_report(scratch)) == {
        "test_nested_abort_does_not_disable_guard": "failed",
        "test_next_test_is_clean": "passed",
    }
    assert "Failed: 1 expect() chain built but never evaluated" in result.stdout.str()


def test_identical_assertions_in_one_test_get_distinct_ids_and_both_report(
    scratch: pytest.Pytester,
) -> None:
    scratch.makepyfile(test_chains=_CHAIN_TESTS)

    scratch.runpytest_subprocess(
        "--attest-report=r.json", "-p", "no:cacheprovider", "-k", "test_identical_assertions"
    )

    report = _report(scratch)
    assert report["id_scheme"] == 2
    assert report["budget_exceeded"] is False
    rows = report["results"]
    assert len(rows) == 2
    first, second = (r["assertion_id"] for r in rows)
    assert first != second
    (test,) = report["tests"]
    assert test["assertion_ids"] == [first, second]
    node_id = test["test_id"]
    spec = (
        expect(AgentResult(trace=TraceBuilder("a").set_output(message="ok").build()))
        .output_contains("ok")
        .assertions[0]
    )
    assert [first, second] == [
        compute_assertion_id(node_id, spec.type, spec.spec, 0),
        compute_assertion_id(node_id, spec.type, spec.spec, 1),
    ]
    for row in rows:
        assert row["scope_id"] == node_id
        assert len(row["spec_hash"]) == 64
    assert rows[0]["spec_hash"] == rows[1]["spec_hash"]


def test_ids_are_stable_across_runs_and_survive_reordering(scratch: pytest.Pytester) -> None:
    body = (
        _PRELUDE
        + """
def test_two_checks(attest):
    attest.evaluate(expect(_result()).{first}.{second})
"""
    )
    ids: list[dict[str, str]] = []
    for first, second in (
        ('output_contains("ok")', 'output_not_contains("zzz")'),
        ('output_not_contains("zzz")', 'output_contains("ok")'),
    ):
        scratch.makepyfile(test_order=body.format(first=first, second=second))
        scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")
        report = _report(scratch)
        ids.append({r["type"] + r["spec_hash"][:8]: r["assertion_id"] for r in report["results"]})

    assert ids[0] == ids[1]
    assert len(ids[0]) == 2


def test_budget_exceeded_flag_is_in_the_report_and_keeps_the_hard_fail(
    scratch: pytest.Pytester, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_MAX_SOFT_FAILURES", "1")
    scratch.makepyfile(
        test_budget=_PRELUDE
        + """
def test_breach_with_hard_fail(attest):
    attest.evaluate(
        expect(_result(cost=5.0))
        .cost_under(0.01, soft=True)
        .output_contains("also-missing", soft=True)
        .output_contains("not-in-output")
    )
"""
    )

    result = scratch.runpytest_subprocess("--attest-report=r.json", "-p", "no:cacheprovider")

    report = _report(scratch)
    result.assert_outcomes(failed=1)
    assert report["budget_exceeded"] is True
    statuses = sorted(r["status"] for r in report["results"])
    assert statuses == ["hard_fail", "soft_fail", "soft_fail"]


def _agent_result(*, budget_exceeded: bool) -> AgentResult:
    row = AssertionResult(assertion_id="a2_" + "0" * 32, status="pass", score=1.0, explanation="")
    return AgentResult(
        trace=TraceBuilder("a").set_output(message="ok").build(),
        assertion_results=[row],
        budget_exceeded=budget_exceeded,
    )


def test_evaluate_batch_result_carries_budget_exceeded() -> None:
    raw: dict[str, Any] = {"results": [], "usage": Usage().to_dict(), "budget_exceeded": True}

    assert EvaluateBatchResult.from_dict(raw).budget_exceeded is True
    assert EvaluateBatchResult.from_dict({**raw, "budget_exceeded": False}).budget_exceeded is False
    assert EvaluateBatchResult.from_dict({k: v for k, v in raw.items() if k != "budget_exceeded"})


def test_build_report_ors_budget_exceeded_across_batches() -> None:
    def record(name: str, *, exceeded: bool) -> TestRecord:
        return TestRecord(
            test_id=f"t.py::{name}",
            name=name,
            file="t.py",
            line=1,
            agent_results=[_agent_result(budget_exceeded=exceeded)],
        )

    quiet = build_report([record("a", exceeded=False)], simulated=False, usage=Usage())
    loud = build_report(
        [record("a", exceeded=False), record("b", exceeded=True)],
        simulated=False,
        usage=Usage(),
    )

    assert quiet["budget_exceeded"] is False
    assert loud["budget_exceeded"] is True
    assert loud["id_scheme"] == 2
    merged = merge_reports([quiet, loud], tests_collected=3, usage=Usage())
    assert merged["budget_exceeded"] is True
    assert merged["id_scheme"] == 2


_NO_FIXTURE_TESTS = (
    _PRELUDE
    + """
MODULE_CHAIN = expect(_result()).output_contains("built at import")

def test_never_evaluated():
    expect(_result()).output_contains("definitely-missing")

def test_plain_test_is_unaffected():
    assert 1 + 1 == 2

def test_plain_test_with_a_stray_result_is_unaffected():
    _result()
"""
)


@pytest.mark.parametrize("workers", [[], ["-n", "2"]], ids=["serial", "xdist"])
def test_chain_built_without_the_attest_fixture_fails_when_never_evaluated(
    scratch: pytest.Pytester, workers: list[str]
) -> None:
    scratch.makepyfile(test_nofixture=_NO_FIXTURE_TESTS)

    result = scratch.runpytest_subprocess(
        "--attest-report=r.json", "-p", "no:cacheprovider", *workers
    )

    result.assert_outcomes(passed=2, failed=1)
    result.stdout.fnmatch_lines(
        ["FAILED *::test_never_evaluated - Failed: 1 expect() chain built but never evaluated"]
    )
