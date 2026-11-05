"""Responses are routed to their request by id, against the real engine.

A verdict always comes from its own request: timeouts,
concurrent evaluations and responses over 64 KiB never attach a verdict to
another test. Each case drives a real ``attest-engine`` built by ``make engine``.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import secrets
import threading
import time
from collections.abc import Generator
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest

from attest import agent, expect
from attest._proto.types import (
    REASON_BUDGET_EXHAUSTED,
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
    STATUS_PASS,
    Assertion,
    SimulatePersona,
    Trace,
)
from attest.client import AttestClient
from attest.engine_manager import EngineManager
from attest.exceptions import EngineTimeoutError
from attest.expect import ExpectChain
from attest.plugin import AttestEngineFixture
from attest.result import AgentResult
from tests.integration.conftest import _engine_binary_path

pytest_plugins = ["pytester"]

pytestmark = pytest.mark.integration

_JUDGE_ENV = {
    "ATTEST_JUDGE_PROVIDER": "openai",
    "ATTEST_JUDGE_API_KEY": "fake",
    "ATTEST_JUDGE_MODEL": "fake-judge",
}
_CLEARED = (
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GOOGLE_API_KEY",
    "GEMINI_API_KEY",
    "ATTEST_SIMULATION",
    "ATTEST_MAX_JUDGE_CALLS",
    "ATTEST_BUDGET_USD",
    "ATTEST_ENGINE_TIMEOUT",
    "ATTEST_JUDGE_BASE_URL",
    *_JUDGE_ENV,
)


@agent("routing-agent")
def _routing_agent(builder: Any, message: str) -> dict[str, str]:
    builder.set_metadata(cost_usd=0.0, total_tokens=1, latency_ms=1)
    return {"message": message}


@pytest.fixture
def engine_path(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> str:
    path = _engine_binary_path()
    if not os.path.exists(path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    for key in _CLEARED:
        monkeypatch.delenv(key, raising=False)
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "cache"))
    return path


@pytest.fixture
def engine(engine_path: str) -> Generator[AttestEngineFixture, None, None]:
    fixture = AttestEngineFixture(engine_path=engine_path, log_level="warn")
    fixture.start()
    yield fixture
    fixture.stop()


def _verdict_chain(message: str, *, passes: bool) -> ExpectChain:
    """A content chain that passes iff ``passes``; the message identifies it."""
    needle = "refund" if passes else "apology"
    return expect(_routing_agent(message=f"{message} refund")).output_contains(needle)


def _slow_deterministic_chain() -> ExpectChain:
    """A deterministic batch too large to be answered within a 0.1 ms ATTEST_ENGINE_TIMEOUT.

    A tiny batch can be answered before the timer fires on a fast machine; 2,000 assertions on
    a 100 KB output take far longer than 0.1 ms to send and evaluate, so the timeout is certain.
    """
    chain = expect(_routing_agent(message="deterministic refund " + "x" * 100_000))
    for _ in range(2_000):
        chain = chain.output_contains("refund")
    return chain


def _statuses(result: AgentResult) -> list[str]:
    return [r.status for r in result.assertion_results]


# ── F1: A hard-fails, B passes, C fails: each verdict stays with its own test ──
# The engine carries drift data inside results, never as a separate stdout line;
# id-less lines are covered by the unit tests in tests/test_client.py.

_DRIFT_SUITE = """
from attest import agent, expect


@agent("drift-agent")
def drift_agent(builder, message):
    builder.set_metadata(cost_usd=0.0, total_tokens=1, latency_ms=1)
    return {"message": message}


def test_a_dynamic_threshold_hard_fails(attest):
    chain = expect(drift_agent(message="all done")).output_contains("apology")
    chain.assertions[-1].spec["threshold"] = "dynamic"
    attest.evaluate(chain)


def test_b_passes(attest):
    attest.evaluate(expect(drift_agent(message="a refund")).output_contains("refund"))


def test_c_fails(attest):
    attest.evaluate(expect(drift_agent(message="a refund")).output_contains("apology"))
"""


def test_dynamic_threshold_failure_leaves_later_verdicts_alone(
    engine_path: str, pytester: pytest.Pytester, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", engine_path)
    pytester.makepyfile(test_drift=_DRIFT_SUITE)

    run = pytester.runpytest_subprocess("-v", "-p", "no:cacheprovider")

    out = run.stdout.str()
    assert "test_a_dynamic_threshold_hard_fails FAILED" in out
    assert "test_b_passes PASSED" in out
    assert "test_c_fails FAILED" in out
    run.assert_outcomes(passed=1, failed=2)


# ── F2: a timeout must not leave its response for the next test ──

_TIMEOUT_SUITE = """
import os

from attest import agent, expect


@agent("timeout-agent")
def timeout_agent(builder, message):
    builder.set_metadata(cost_usd=0.0, total_tokens=1, latency_ms=1)
    return {"message": message}


def test_a_times_out(attest):
    # A request this large cannot be answered within 0.1 ms, so the timeout always fires.
    chain = expect(timeout_agent(message="a refund " + "x" * 100_000))
    for _ in range(2_000):
        chain = chain.output_contains("refund")
    os.environ["ATTEST_ENGINE_TIMEOUT"] = "0.0001"
    try:
        attest.evaluate(chain)
    finally:
        del os.environ["ATTEST_ENGINE_TIMEOUT"]


def test_b_fails_after_the_timeout(attest):
    attest.evaluate(expect(timeout_agent(message="a refund")).output_contains("apology"))


def test_c_passes(attest):
    attest.evaluate(expect(timeout_agent(message="a refund")).output_contains("refund"))
"""


def test_timeout_leaves_the_next_failing_test_failing(
    engine_path: str, pytester: pytest.Pytester, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", engine_path)
    pytester.makepyfile(test_timeout=_TIMEOUT_SUITE)

    run = pytester.runpytest_subprocess("-v", "-p", "no:cacheprovider")

    out = run.stdout.str()
    assert "test_a_times_out FAILED" in out
    assert "EngineTimeoutError" in out
    assert "test_b_fails_after_the_timeout FAILED" in out
    assert "test_c_passes PASSED" in out
    run.assert_outcomes(passed=1, failed=2)


# ── Concurrent evaluations each get their own verdict ──


@pytest.mark.asyncio
async def test_gather_of_chains_gives_each_its_own_verdict(engine: AttestEngineFixture) -> None:
    wanted = [i % 3 != 0 for i in range(12)]
    chains = [_verdict_chain(f"chain-{i}", passes=p) for i, p in enumerate(wanted)]

    results = await asyncio.gather(*(engine.inspect_async(c) for c in chains))

    got = [_statuses(r) for r in results]
    assert got == [[STATUS_PASS if p else STATUS_HARD_FAIL] for p in wanted]
    for chain, result in zip(chains, results, strict=True):
        assert [r.assertion_id for r in result.assertion_results] == [
            a.assertion_id for a in chain.assertions
        ]


def test_eight_threads_inspecting_each_get_their_own_verdict(
    engine: AttestEngineFixture,
) -> None:
    wanted = [i % 2 == 0 for i in range(8)]

    def inspect(i: int) -> AgentResult:
        return engine.inspect(_verdict_chain(f"thread-{i}", passes=wanted[i]))

    with ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(inspect, range(8)))

    assert [_statuses(r) for r in results] == [
        [STATUS_PASS if p else STATUS_HARD_FAIL] for p in wanted
    ]


# ── F14: a response over 64 KiB ──


def test_1500_assertions_on_a_96kb_output_evaluate(engine: AttestEngineFixture) -> None:
    message = "x" * 96_000 + " refund"
    chain = expect(_routing_agent(message=message))
    for _ in range(1500):
        chain.output_contains("refund")

    result = engine.inspect(chain)

    assert len(result.assertion_results) == 1500
    assert {r.status for r in result.assertion_results} == {STATUS_PASS}


# ── C1: a timeout restarts the engine without refilling the judge budget ──


class _FakeJudge:
    """OpenAI-compatible judge on loopback that counts calls and can stall them."""

    def __init__(self) -> None:
        self.calls = 0
        self.stall_seconds = 0.0
        self._lock = threading.Lock()
        self._release = threading.Event()
        judge = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 (http.server API)
                length = int(self.headers["Content-Length"])
                request = json.loads(self.rfile.read(length))
                with judge._lock:
                    judge.calls += 1
                    stall = judge.stall_seconds
                if stall:
                    judge._release.wait(stall)
                verdict = {"score": 0.95, "explanation": "scripted"}
                body = json.dumps(
                    {
                        "id": f"chatcmpl-{secrets.token_hex(8)}",
                        "object": "chat.completion",
                        "created": int(time.time()),
                        "model": request["model"],
                        "choices": [
                            {
                                "index": 0,
                                "finish_reason": "stop",
                                "message": {"role": "assistant", "content": json.dumps(verdict)},
                            }
                        ],
                        "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
                    }
                ).encode()
                try:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                except OSError:
                    pass  # the engine was killed while this call stalled

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                return

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._server.daemon_threads = True
        threading.Thread(target=self._server.serve_forever, daemon=True).start()

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self._server.server_address[1]}/v1"

    def close(self) -> None:
        self._release.set()
        self._server.shutdown()
        self._server.server_close()


@pytest.fixture
def judge(monkeypatch: pytest.MonkeyPatch) -> Generator[_FakeJudge, None, None]:
    server = _FakeJudge()
    for key, value in _JUDGE_ENV.items():
        monkeypatch.setenv(key, value)
    monkeypatch.setenv("ATTEST_JUDGE_BASE_URL", server.base_url)
    monkeypatch.setenv("ATTEST_MAX_JUDGE_CALLS", "2")
    yield server
    server.close()


def _judged(message: str) -> ExpectChain:
    return expect(_routing_agent(message=message)).passes_judge("Is it helpful?")


def _reasons(result: AgentResult) -> list[tuple[str, str | None]]:
    return [(r.status, r.reason) for r in result.assertion_results]


def test_timeout_during_a_judge_call_never_exceeds_the_call_budget(
    engine_path: str, judge: _FakeJudge, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_TIMEOUT", "2")
    judge.stall_seconds = 20.0
    fixture = AttestEngineFixture(engine_path=engine_path, log_level="warn")
    fixture.start()
    try:
        with pytest.raises(EngineTimeoutError):
            fixture.inspect(_judged("first judged output"))
        judge.stall_seconds = 0.0

        after = fixture.inspect(_judged("second judged output"))
        third = fixture.inspect(_judged("third judged output"))
    finally:
        fixture.stop()

    expected = [(STATUS_NOT_EVALUATED, REASON_BUDGET_EXHAUSTED)]
    assert _reasons(after) == expected
    assert _reasons(third) == expected
    assert judge.calls <= 2
    assert judge.calls == 1


def test_timeout_outside_the_judge_keeps_the_remaining_call_budget(
    engine_path: str, judge: _FakeJudge, monkeypatch: pytest.MonkeyPatch
) -> None:
    fixture = AttestEngineFixture(engine_path=engine_path, log_level="warn")
    fixture.start()
    try:
        first = fixture.inspect(_judged("first judged output"))
        monkeypatch.setenv("ATTEST_ENGINE_TIMEOUT", "0.0001")
        with pytest.raises(EngineTimeoutError):
            fixture.inspect(_slow_deterministic_chain())
        monkeypatch.delenv("ATTEST_ENGINE_TIMEOUT")

        second = fixture.inspect(_judged("second judged output"))
        third = fixture.inspect(_judged("third judged output"))
    finally:
        fixture.stop()

    assert _statuses(first) == [STATUS_PASS]
    assert _statuses(second) == [STATUS_PASS]
    assert _reasons(third) == [(STATUS_NOT_EVALUATED, REASON_BUDGET_EXHAUSTED)]
    assert judge.calls == 2


def test_unobserved_judge_spend_holds_the_whole_budget_at_restart(
    engine_path: str, judge: _FakeJudge, monkeypatch: pytest.MonkeyPatch
) -> None:
    """generate_user_message returns no usage, so its calls are unknown spend."""
    persona = SimulatePersona(name="p", system_prompt="s", style="terse", temperature=0.0)
    fixture = AttestEngineFixture(engine_path=engine_path, log_level="warn")
    fixture.start()
    try:
        for _ in range(2):
            fixture._run_on_engine_loop(fixture.client.generate_user_message(persona, []))
        monkeypatch.setenv("ATTEST_ENGINE_TIMEOUT", "0.0001")
        with pytest.raises(EngineTimeoutError):
            fixture.inspect(_slow_deterministic_chain())
        monkeypatch.delenv("ATTEST_ENGINE_TIMEOUT")

        after = fixture.inspect(_judged("judged output after the restart"))
    finally:
        fixture.stop()

    assert _reasons(after) == [(STATUS_NOT_EVALUATED, REASON_BUDGET_EXHAUSTED)]
    assert judge.calls == 2


@pytest.mark.asyncio
async def test_direct_engine_manager_and_client_shut_down_cleanly(
    engine_path: str, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.WARNING, logger="attest.engine")
    trace = Trace(trace_id="trc_clean", output={"message": "refund"})
    assertion = Assertion(
        assertion_id="a1",
        type="content",
        spec={"target": "output.message", "check": "contains", "value": "refund"},
    )

    async with EngineManager(engine_path) as manager:
        client = AttestClient(manager)
        result = await client.evaluate_batch(trace, [assertion])
        process = manager._process

    assert [r.status for r in result.results] == [STATUS_PASS]
    assert process is not None
    assert process.returncode == 0  # exited through shutdown, not SIGTERM (-15)
    assert "Shutdown request failed" not in caplog.text
