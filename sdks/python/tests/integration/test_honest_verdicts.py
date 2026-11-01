"""End-to-end pytest runs against the real engine: red means red.

Each case runs a generated test module in a pytest subprocess with no
provider keys, so outcomes come from the engine's real verdicts.
"""

from __future__ import annotations

import os

import pytest

from tests.integration.conftest import _engine_binary_path

pytest_plugins = ["pytester"]

_AGENT = """
from attest import agent, expect


@agent("refund-agent")
def refund_agent(builder, order_id, loops=1):
    for _ in range(loops):
        builder.add_tool_call("lookup_order", args={"order_id": order_id},
                              result={"status": "delivered", "amount": 89.99})
    builder.add_tool_call("process_refund", args={"order_id": order_id},
                          result={"refund_id": "RFD-001"})
    builder.set_metadata(cost_usd=0.005, total_tokens=150, latency_ms=1200)
    return {"message": "Your refund of $89.99 has been processed.",
            "structured": {"refund_id": "RFD-001"}}


def test_loop_violated(attest):
    result = refund_agent(order_id="ORD-1", loops=3)
    attest.evaluate(
        expect(result)
        .output_matches_schema({"type": "object", "required": ["refund_id"]})
        .cost_under(0.05)
        .no_tool_loops("lookup_order", max_repetitions=1)
        .output_contains("refund")
        .passes_judge("Is the response helpful and accurate?")
    )


def test_constraint_violated(attest):
    attest.evaluate(expect(refund_agent(order_id="ORD-2")).cost_under(0.001))


def test_content_violated(attest):
    attest.evaluate(expect(refund_agent(order_id="ORD-3")).output_contains("apology"))


def test_judge_only(attest):
    attest.evaluate(expect(refund_agent(order_id="ORD-4")).passes_judge("Helpful?"))
"""

_KEYS = (
    "OPENAI_API_KEY",
    "ATTEST_OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "ATTEST_GEMINI_API_KEY",
    "ATTEST_JUDGE_PROVIDER",
)


@pytest.fixture
def scratch(pytester: pytest.Pytester, monkeypatch: pytest.MonkeyPatch) -> pytest.Pytester:
    path = _engine_binary_path()
    if not os.path.exists(path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    for key in _KEYS:
        monkeypatch.delenv(key, raising=False)
    monkeypatch.delenv("ATTEST_SIMULATION", raising=False)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", path)
    monkeypatch.setenv("COLUMNS", "200")
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(pytester.path / ".cache"))
    pytester.makepyfile(test_refund=_AGENT)
    return pytester


@pytest.mark.integration
@pytest.mark.parametrize("simulation", [False, True], ids=["no-keys", "simulation"])
def test_violated_contracts_fail_and_judge_only_skips(
    scratch: pytest.Pytester, monkeypatch: pytest.MonkeyPatch, simulation: bool
) -> None:
    if simulation:
        monkeypatch.setenv("ATTEST_SIMULATION", "1")
    reason = "simulation" if simulation else "provider_not_configured"

    run = scratch.runpytest_subprocess("-v", "-rs", "-p", "no:cacheprovider")

    run.assert_outcomes(failed=3, skipped=1)
    assert run.ret == 1
    out = run.stdout.str()
    assert "trace.loop_detection" in out
    assert "not evaluated: short_circuited" in out
    run.stdout.fnmatch_lines(
        [
            f"*test_judge_only SKIPPED (not evaluated: {reason})*",
            f"SKIPPED [[]1[]] test_refund.py:*: not evaluated: {reason}",
        ]
    )


@pytest.mark.integration
def test_strict_fails_not_evaluated(scratch: pytest.Pytester) -> None:
    run = scratch.runpytest_subprocess("-p", "no:cacheprovider", "--attest-strict", "-k", "judge")

    run.assert_outcomes(failed=1)
    run.stdout.fnmatch_lines(["*--attest-strict: not evaluated: provider_not_configured*"])


@pytest.mark.integration
def test_attest_strict_env_fails_not_evaluated(
    scratch: pytest.Pytester, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_STRICT", "1")

    run = scratch.runpytest_subprocess("-p", "no:cacheprovider", "-k", "judge")

    run.assert_outcomes(failed=1)


_TRIALS = """
from attest import expect
from attest.simulation import repeat
from test_refund import refund_agent


@repeat(3)
def judge_trials(attest):
    return attest.inspect(expect(refund_agent(order_id="ORD-T")).passes_judge("Helpful?"))


def test_trial_loop(attest):
    attest.enforce(judge_trials(attest))
"""


@pytest.mark.integration
@pytest.mark.parametrize("strict", [False, True], ids=["default", "strict"])
def test_all_not_evaluated_trial_loop_skips_or_fails_strict(
    scratch: pytest.Pytester, strict: bool
) -> None:
    scratch.makepyfile(test_trials=_TRIALS)
    args = ["-p", "no:cacheprovider", "-rs", "test_trials.py"]

    run = scratch.runpytest_subprocess(*args, *(["--attest-strict"] if strict else []))

    if strict:
        run.assert_outcomes(failed=1)
        run.stdout.fnmatch_lines(["*--attest-strict: not evaluated: provider_not_configured ×3*"])
    else:
        run.assert_outcomes(skipped=1)
        run.stdout.fnmatch_lines(["*not evaluated: provider_not_configured ×3*"])
