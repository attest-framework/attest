"""ContinuousEvalRunner delivers drift alerts, against the real engine.

A scripted OpenAI-compatible judge scores each trace by its output, so ten good runs followed
by a bad one drift deterministically; a local HTTP server stands in for the webhook receiver.
Each case drives a real ``attest-engine`` built by ``make engine``. No paid calls.
"""

from __future__ import annotations

import asyncio
import json
import os
import threading
import time
from collections.abc import AsyncGenerator, Generator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest
import pytest_asyncio

import attest.continuous as continuous
from attest._proto.types import STATUS_HARD_FAIL, Assertion, Trace
from attest.client import AttestClient
from attest.continuous import ContinuousEvalRunner
from attest.engine_manager import EngineManager
from tests.integration.conftest import _engine_binary_path

pytestmark = [pytest.mark.integration, pytest.mark.asyncio]

_GOOD_SCORE = 0.9
_BAD_SCORE = 0.3
_ALERT_DEADLINE_S = 5.0
_ENGINE_ENV = {
    "ATTEST_JUDGE_PROVIDER": "openai",
    "ATTEST_JUDGE_API_KEY": "fake",
    "ATTEST_JUDGE_MODEL": "fake-judge",
    "ATTEST_RATE_LIMIT_OPENAI": "60000",
    "ATTEST_RATE_LIMIT_OPENAI_BURST": "1000",
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
)


class _Server:
    """A local HTTP server running ``handler`` on a thread."""

    def __init__(self, handler: type[BaseHTTPRequestHandler]) -> None:
        self._server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.port: int = self._server.server_address[1]
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()


class _Judge(BaseHTTPRequestHandler):
    """Scores ``bad-*`` outputs low and everything else high; every output is distinct."""

    def do_POST(self) -> None:  # noqa: N802 (http.server API)
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        prompt = "\n".join(str(m.get("content")) for m in request["messages"])
        score = _BAD_SCORE if "bad-" in prompt else _GOOD_SCORE
        content = json.dumps({"score": score, "explanation": "scripted"})
        body = json.dumps(
            {
                "model": request["model"],
                "choices": [{"message": {"role": "assistant", "content": content}}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5},
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: Any) -> None:
        pass


class _Receiver:
    """A webhook receiver recording every JSON body; ``status`` and ``delay_s`` are tunable."""

    def __init__(self) -> None:
        self.bodies: list[dict[str, Any]] = []
        self.status = 200
        self.delay_s = 0.0
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 (http.server API)
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                time.sleep(outer.delay_s)
                outer.bodies.append(body)
                self.send_response(outer.status)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, format: str, *args: Any) -> None:
                pass

        self._server = _Server(Handler)
        self.url = self._server.url

    def close(self) -> None:
        self._server.close()


@pytest.fixture
def receiver() -> Generator[_Receiver, None, None]:
    server = _Receiver()
    yield server
    server.close()


@pytest_asyncio.fixture
async def client(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> AsyncGenerator[AttestClient, None]:
    path = _engine_binary_path()
    if not os.path.exists(path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    judge = _Server(_Judge)
    for key in _CLEARED:
        monkeypatch.delenv(key, raising=False)
    for key, value in _ENGINE_ENV.items():
        monkeypatch.setenv(key, value)
    monkeypatch.setenv("ATTEST_JUDGE_BASE_URL", f"{judge.url}/v1")
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "cache"))
    try:
        async with EngineManager(path) as manager:
            yield AttestClient(manager)
    finally:
        judge.close()


def _judge_assertion() -> Assertion:
    return Assertion(
        assertion_id="placeholder",
        type="llm_judge",
        spec={
            "target": "output.message",
            "criteria": "Is the answer helpful?",
            "threshold": "dynamic",
        },
    )


def _trace(kind: str, n: int) -> Trace:
    return Trace(trace_id=f"{kind}-{n}", output={"message": f"{kind}-{n} refund answer"})


def _runner(
    client: AttestClient, receiver: _Receiver | None, **kwargs: Any
) -> ContinuousEvalRunner:
    return ContinuousEvalRunner(
        client=client,
        assertions=[_judge_assertion()],
        name="refund-monitor",
        alert_webhook=receiver.url if receiver is not None else None,
        **kwargs,
    )


async def _wait_for(predicate: Any, deadline_s: float) -> bool:
    end = time.monotonic() + deadline_s
    while time.monotonic() < end:
        if predicate():
            return True
        await asyncio.sleep(0.05)
    return bool(predicate())


async def _ten_good_runs(runner: ContinuousEvalRunner) -> None:
    for n in range(10):
        await runner.evaluate_trace(_trace("good", n))


async def test_drifting_trace_posts_an_alert_to_the_receiver_within_five_seconds(
    client: AttestClient, receiver: _Receiver
) -> None:
    runner = _runner(client, receiver)
    await _ten_good_runs(runner)
    assert receiver.bodies == []
    await runner.start()

    started = time.monotonic()
    await runner.submit(_trace("bad", 0))
    delivered = await _wait_for(lambda: bool(receiver.bodies), _ALERT_DEADLINE_S)
    elapsed = time.monotonic() - started
    await runner.stop()

    assert delivered, "no alert reached the receiver"
    assert elapsed < _ALERT_DEADLINE_S
    (alert,) = receiver.bodies
    assert alert["runner"] == "refund-monitor"
    assert alert["assertion_id"] == runner._assertions[0].assertion_id
    assert alert["status"] == continuous.STATUS_DRIFT_DETECTED
    assert alert["result_status"] == STATUS_HARD_FAIL
    assert alert["score"] == pytest.approx(_BAD_SCORE)
    assert alert["baseline_mean"] == pytest.approx(_GOOD_SCORE)
    assert alert["baseline_count"] == 10
    assert alert["static_threshold"] == 0.8


async def test_stable_scores_post_no_alert(client: AttestClient, receiver: _Receiver) -> None:
    runner = _runner(client, receiver)

    for n in range(12):
        await runner.evaluate_trace(_trace("good", n))

    assert receiver.bodies == []


async def test_receiver_returning_500_emits_a_dispatch_error_and_the_runner_survives(
    client: AttestClient, receiver: _Receiver
) -> None:
    errors: list[Exception] = []
    runner = _runner(client, receiver, on_error=errors.append)
    await _ten_good_runs(runner)
    receiver.status = 500
    await runner.start()

    await runner.submit(_trace("bad", 0))
    await runner.submit(_trace("bad", 1))
    await runner.stop()

    assert len(receiver.bodies) == 2, "the loop stopped after the first failed dispatch"
    assert len(errors) == 2
    assert all(isinstance(e, continuous.AlertDispatchError) for e in errors)
    assert "500" in str(errors[0])


async def test_receiver_down_emits_a_dispatch_error(client: AttestClient) -> None:
    dead = _Receiver()
    dead.close()
    errors: list[Exception] = []
    runner = _runner(client, dead, on_error=errors.append)
    await _ten_good_runs(runner)

    result = await runner.evaluate_trace(_trace("bad", 0))

    assert result is not None
    assert len(errors) == 1
    assert isinstance(errors[0], continuous.AlertDispatchError)


async def test_stop_waits_for_the_in_flight_evaluation_and_its_dispatch(
    client: AttestClient, receiver: _Receiver
) -> None:
    runner = _runner(client, receiver)
    await _ten_good_runs(runner)
    receiver.delay_s = 1.0
    await runner.start()
    await runner.submit(_trace("bad", 0))
    await asyncio.sleep(0.2)  # the evaluation is under way; the alert is not yet delivered

    await runner.stop()

    assert len(receiver.bodies) == 1
