"""Tests for AttestClient."""

from __future__ import annotations

import asyncio
import json
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from attest._proto.types import Assertion, BatchBudget, EvaluateBatchResult, Trace, Usage
from attest.client import AttestClient
from attest.exceptions import ProtocolDesyncError


def test_client_instantiation() -> None:
    """AttestClient can be created with an engine manager."""
    engine = MagicMock()
    client = AttestClient(engine)
    assert client._request_id == 0
    assert client._pending == {}
    assert client._reader_task is None


def test_client_imports() -> None:
    """Verify AttestClient and protocol types are importable together."""
    assert AttestClient is not None
    assert Assertion is not None
    assert Trace is not None


class _PipedEngine:
    """Engine stand-in whose stdout answers what stdin receives, via a queue.

    ``respond`` maps each request (decoded) to the lines to emit; the default
    echoes ``{"data": <method>}`` under the request's id.
    """

    def __init__(self) -> None:
        self.lines: asyncio.Queue[bytes] = asyncio.Queue()
        self.requests: list[dict[str, Any]] = []
        stdin = MagicMock()
        stdin.write = self._write
        stdin.drain = AsyncMock()
        stdout = MagicMock()
        stdout.readline = self.lines.get
        self._process = MagicMock(stdin=stdin, stdout=stdout)
        self._init_result = None
        self.spawn_env: dict[str, str] = {}

    def attach_client(self, client: AttestClient) -> None:
        self.client = client

    def _write(self, data: bytes) -> None:
        request = json.loads(data)
        self.requests.append(request)
        for line in self.respond(request):
            self.lines.put_nowait(line)

    def respond(self, request: dict[str, Any]) -> list[bytes]:
        reply = {"jsonrpc": "2.0", "id": request["id"], "result": {"data": request["method"]}}
        return [json.dumps(reply).encode() + b"\n"]


@pytest.mark.asyncio
async def test_send_request_starts_the_reader_on_first_use() -> None:
    """No sequential fallback: the first request starts the id-routing reader."""
    engine = _PipedEngine()
    client = AttestClient(engine)  # type: ignore[arg-type]
    assert client._reader_task is None

    result = await client.send_request("method_a", {})

    assert result == {"data": "method_a"}
    assert client._reader_task is not None
    await client.stop_reader()


@pytest.mark.asyncio
async def test_send_request_id_correlation() -> None:
    """send_request assigns incrementing IDs and resolves futures by ID."""
    engine = _PipedEngine()
    client = AttestClient(engine)  # type: ignore[arg-type]
    client.start_reader()

    result1 = await client.send_request("method_a", {})
    result2 = await client.send_request("method_b", {})

    await client.stop_reader()

    assert result1 == {"data": "method_a"}
    assert result2 == {"data": "method_b"}
    assert client._request_id == 2


@pytest.mark.asyncio
async def test_send_request_drops_lines_without_id_and_keeps_the_real_response() -> None:
    """A notification before the response is dropped, never taken as the response."""

    class Noisy(_PipedEngine):
        def respond(self, request: dict[str, Any]) -> list[bytes]:
            note = {"jsonrpc": "2.0", "method": "drift_alert", "params": {"x": 1}}
            return [json.dumps(note).encode() + b"\n", *super().respond(request)]

    engine = Noisy()
    client = AttestClient(engine)  # type: ignore[arg-type]

    result = await client.send_request("method_a", {})

    await client.stop_reader()
    assert result == {"data": "method_a"}
    assert client.protocol_diagnostics() == ()


@pytest.mark.asyncio
async def test_submit_plugin_result_returns_accepted_flag() -> None:
    """submit_plugin_result extracts the 'accepted' bool from the response."""
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value={"accepted": True})  # type: ignore[method-assign]

    result = await client.submit_plugin_result(
        trace_id="trc_1",
        plugin_name="my_plugin",
        assertion_id="assert_abc",
        status="pass",
        score=1.0,
        explanation="looks good",
    )

    assert result is True


@pytest.mark.asyncio
async def test_submit_plugin_result_false_when_not_accepted() -> None:
    """submit_plugin_result returns False when engine rejects the result."""
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value={"accepted": False})  # type: ignore[method-assign]

    result = await client.submit_plugin_result(
        trace_id="trc_1",
        plugin_name="plugin",
        assertion_id="a1",
        status="hard_fail",
        score=0.0,
        explanation="failed",
    )

    assert result is False


@pytest.mark.asyncio
async def test_evaluate_batch_parses_result() -> None:
    """evaluate_batch sends correct params and returns EvaluateBatchResult."""
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(
        return_value={
            "results": [
                {
                    "assertion_id": "a1",
                    "status": "pass",
                    "score": 1.0,
                    "explanation": "ok",
                    "cost": 0.0,
                    "duration_ms": 10,
                }
            ],
            "total_cost": 0.0,
            "usage": Usage().to_dict(),
            "total_duration_ms": 10,
        }
    )  # type: ignore[method-assign]

    trace = Trace(trace_id="trc_1", output={"message": "hello"})
    assertions = [
        Assertion(assertion_id="a1", type="content", spec={"check": "contains", "value": "hello"})
    ]

    batch_result = await client.evaluate_batch(trace, assertions)

    assert isinstance(batch_result, EvaluateBatchResult)
    assert len(batch_result.results) == 1
    assert batch_result.results[0].assertion_id == "a1"
    assert batch_result.results[0].status == "pass"

    call_args: Any = client.send_request.call_args
    assert call_args[0][0] == "evaluate_batch"
    params = call_args[0][1]
    assert params["trace"]["trace_id"] == "trc_1"
    assert len(params["assertions"]) == 1
    assert "budget" not in params


@pytest.mark.asyncio
async def test_evaluate_batch_with_budget_sends_set_limits_only() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(
        return_value={"results": [], "usage": Usage(judge_calls=2).to_dict()}
    )  # type: ignore[method-assign]

    batch_result = await client.evaluate_batch(
        Trace(trace_id="trc_1", output={}), [], budget=BatchBudget(max_cost_usd=0.25)
    )

    params = client.send_request.call_args[0][1]
    assert params["budget"] == {"max_cost_usd": 0.25}
    assert batch_result.usage == Usage(judge_calls=2)


@pytest.mark.asyncio
async def test_reader_fails_all_pending_on_closed_stdout() -> None:
    """Reader loop fails all pending futures when stdout closes."""
    engine = MagicMock()

    stdout_mock = MagicMock()
    stdout_mock.readline = AsyncMock(return_value=b"")  # EOF immediately

    stdin_mock = MagicMock()
    stdin_mock.write = MagicMock()
    stdin_mock.drain = AsyncMock()

    process_mock = MagicMock()
    process_mock.stdout = stdout_mock
    process_mock.stdin = stdin_mock

    engine._process = process_mock

    client = AttestClient(engine)

    # Manually register a pending future to verify it gets cancelled on EOF
    loop = asyncio.get_event_loop()
    fut: asyncio.Future[Any] = loop.create_future()
    client._pending[99] = fut

    client.start_reader()
    await asyncio.sleep(0.05)

    assert fut.done()
    assert isinstance(fut.exception(), ConnectionError)


def _result_line(request: dict[str, Any], assertion_ids: list[str]) -> bytes:
    results = [
        {"assertion_id": a, "status": "pass", "score": 1.0, "explanation": ""}
        for a in assertion_ids
    ]
    result = {"results": results, "usage": Usage().to_dict()}
    return json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode() + b"\n"


@pytest.mark.asyncio
async def test_evaluate_batch_raises_desync_when_returned_ids_differ_from_sent() -> None:
    """A response for other assertions is never accepted as this batch's verdict."""

    class Mismatched(_PipedEngine):
        def respond(self, request: dict[str, Any]) -> list[bytes]:
            return [_result_line(request, ["assert_other"])]

    client = AttestClient(Mismatched())  # type: ignore[arg-type]
    sent = [Assertion(assertion_id="a1", type="content", spec={})]

    with pytest.raises(ProtocolDesyncError, match="a1"):
        await client.evaluate_batch(Trace(trace_id="t", output={}), sent)

    await client.stop_reader()


@pytest.mark.asyncio
async def test_evaluate_batch_accepts_results_in_any_order() -> None:
    class Reversed(_PipedEngine):
        def respond(self, request: dict[str, Any]) -> list[bytes]:
            ids = [a["assertion_id"] for a in request["params"]["assertions"]]
            return [_result_line(request, ids[::-1])]

    client = AttestClient(Reversed())  # type: ignore[arg-type]
    sent = [Assertion(assertion_id=i, type="content", spec={}) for i in ("a1", "a2")]

    result = await client.evaluate_batch(Trace(trace_id="t", output={}), sent)

    assert [r.assertion_id for r in result.results] == ["a2", "a1"]
    await client.stop_reader()


def _client_with_limits(env: dict[str, str]) -> AttestClient:
    engine = _PipedEngine()
    engine.spawn_env = env
    return AttestClient(engine)  # type: ignore[arg-type]


def test_restart_budget_is_the_limit_minus_returned_usage() -> None:
    client = _client_with_limits({"ATTEST_MAX_JUDGE_CALLS": "5", "ATTEST_BUDGET_USD": "1.5"})
    client._spent = Usage(judge_calls=2, cost_usd=0.5)

    assert client._remaining_budget_env() == {
        "ATTEST_MAX_JUDGE_CALLS": "3",
        "ATTEST_BUDGET_USD": "1.0",
    }


def test_restart_budget_is_zero_when_a_lost_request_could_spend_it_all() -> None:
    client = _client_with_limits({"ATTEST_MAX_JUDGE_CALLS": "2", "ATTEST_BUDGET_USD": "1.5"})
    client._judge_calls_exhausted = True
    client._usd_exhausted = True

    assert client._remaining_budget_env() == {
        "ATTEST_MAX_JUDGE_CALLS": "0",
        "ATTEST_BUDGET_USD": "0.0",
    }


def test_restart_budget_never_goes_negative() -> None:
    client = _client_with_limits({"ATTEST_MAX_JUDGE_CALLS": "1"})
    client._spent = Usage(judge_calls=4)

    assert client._remaining_budget_env() == {"ATTEST_MAX_JUDGE_CALLS": "0"}


def test_restart_budget_is_empty_without_configured_limits() -> None:
    assert _client_with_limits({})._remaining_budget_env() == {}


@pytest.mark.asyncio
async def test_concurrent_timeouts_restart_the_engine_once(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Two requests timing out together restart the engine once, with the lost budget."""
    from attest.exceptions import EngineTimeoutError

    class Silent(_PipedEngine):
        restarts: list[dict[str, str]]

        def respond(self, request: dict[str, Any]) -> list[bytes]:
            return []

        async def restart(self, env_overrides: dict[str, str]) -> None:
            self.restarts.append(env_overrides)

    engine = Silent()
    engine.restarts = []
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "3"}
    monkeypatch.setenv("ATTEST_ENGINE_TIMEOUT", "0.05")
    client = AttestClient(engine)  # type: ignore[arg-type]

    first, second = await asyncio.gather(
        client.send_request("evaluate_batch", {"assertions": [{"type": "llm_judge"}]}),
        client.send_request("evaluate_batch", {"assertions": [{"type": "content"}]}),
        return_exceptions=True,
    )

    assert isinstance(first, EngineTimeoutError)
    assert isinstance(second, EngineTimeoutError)
    assert engine.restarts == [{"ATTEST_MAX_JUDGE_CALLS": "0"}]
    await client.stop_reader()


class _RestartingEngine(_PipedEngine):
    """Records restarts and updates ``spawn_env`` the way the real EngineManager does."""

    def __init__(self) -> None:
        super().__init__()
        self.restarts: list[dict[str, str]] = []

    async def restart(self, env_overrides: dict[str, str]) -> None:
        self.restarts.append(env_overrides)
        self.spawn_env = {**self.spawn_env, **env_overrides}


@pytest.mark.asyncio
async def test_second_restart_budget_is_computed_from_the_configured_limit() -> None:
    engine = _RestartingEngine()
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "10"}
    client = AttestClient(engine)  # type: ignore[arg-type]
    client._ensure_reader()

    client._spent = Usage(judge_calls=3)
    await client._restart_after_timeout(0, "evaluate_batch", 1.0)
    client._spent = Usage(judge_calls=5)  # two more calls on the second engine
    await client._restart_after_timeout(1, "evaluate_batch", 1.0)

    assert engine.restarts == [
        {"ATTEST_MAX_JUDGE_CALLS": "7"},
        {"ATTEST_MAX_JUDGE_CALLS": "5"},
    ]
    await client.stop_reader()


class _Silent(_RestartingEngine):
    def respond(self, request: dict[str, Any]) -> list[bytes]:
        return []


@pytest.mark.asyncio
async def test_timed_out_batch_with_a_per_batch_cap_holds_only_that_cap(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from attest.exceptions import EngineTimeoutError

    engine = _Silent()
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "5"}
    monkeypatch.setenv("ATTEST_ENGINE_TIMEOUT", "0.05")
    client = AttestClient(engine)  # type: ignore[arg-type]

    with pytest.raises(EngineTimeoutError):
        await client.send_request(
            "evaluate_batch",
            {"assertions": [{"type": "llm_judge"}], "budget": {"max_judge_calls": 2}},
        )

    assert engine.restarts == [{"ATTEST_MAX_JUDGE_CALLS": "3"}]
    await client.stop_reader()


@pytest.mark.asyncio
async def test_generate_user_message_reply_makes_the_spend_unknown() -> None:
    engine = _RestartingEngine()
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "5"}
    client = AttestClient(engine)  # type: ignore[arg-type]

    await client.send_request("generate_user_message", {})

    assert client._remaining_budget_env() == {"ATTEST_MAX_JUDGE_CALLS": "0"}
    await client.stop_reader()


@pytest.mark.asyncio
async def test_engine_error_reply_to_a_judge_batch_makes_the_spend_unknown() -> None:
    from attest._proto.codec import ProtocolError

    class Failing(_RestartingEngine):
        def respond(self, request: dict[str, Any]) -> list[bytes]:
            error = {"code": -32000, "message": "boom"}
            return [json.dumps({"jsonrpc": "2.0", "id": request["id"], "error": error}).encode()]

    engine = Failing()
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "5"}
    client = AttestClient(engine)  # type: ignore[arg-type]

    with pytest.raises(ProtocolError):
        await client.send_request("evaluate_batch", {"assertions": [{"type": "llm_judge"}]})

    assert client._remaining_budget_env() == {"ATTEST_MAX_JUDGE_CALLS": "0"}
    await client.stop_reader()


@pytest.mark.asyncio
async def test_dropped_reply_of_a_cancelled_judge_batch_makes_the_spend_unknown() -> None:
    engine = _Silent()
    engine.spawn_env = {"ATTEST_MAX_JUDGE_CALLS": "5"}
    client = AttestClient(engine)  # type: ignore[arg-type]
    task = asyncio.ensure_future(
        client.send_request("evaluate_batch", {"assertions": [{"type": "llm_judge"}]})
    )
    await asyncio.sleep(0.05)
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    late = {"jsonrpc": "2.0", "id": 1, "result": {"results": [], "usage": {}}}
    engine.lines.put_nowait(json.dumps(late).encode() + b"\n")
    await asyncio.sleep(0.05)

    assert client._remaining_budget_env() == {"ATTEST_MAX_JUDGE_CALLS": "0"}
    await client.stop_reader()


class _NotifyingEngine(_PipedEngine):
    """Emits id-less JSON-RPC notification lines before each response."""

    notifications = 0

    def respond(self, request: dict[str, Any]) -> list[bytes]:
        note = {"jsonrpc": "2.0", "method": "progress", "params": {"n": 1}}
        lines = [json.dumps(note).encode() + b"\n"] * self.notifications
        return [*lines, *super().respond(request)]


@pytest.mark.asyncio
async def test_id_less_notification_before_a_response_does_not_replace_it() -> None:
    engine = _NotifyingEngine()
    engine.notifications = 1
    client = AttestClient(engine)  # type: ignore[arg-type]

    result = await client.send_request("evaluate_batch", {})

    assert result == {"data": "evaluate_batch"}
    await client.stop_reader()


@pytest.mark.asyncio
async def test_id_less_lines_at_the_desync_threshold_do_not_latch_desync() -> None:
    from attest._proto.diagnostics import DEFAULT_DESYNC_THRESHOLD

    engine = _NotifyingEngine()
    engine.notifications = DEFAULT_DESYNC_THRESHOLD + 2
    client = AttestClient(engine)  # type: ignore[arg-type]

    first = await client.send_request("evaluate_batch", {})
    second = await client.send_request("evaluate_batch", {})

    assert first == second == {"data": "evaluate_batch"}
    assert client._desynced is False
    assert client.protocol_diagnostics() == ()
    await client.stop_reader()


@pytest.mark.asyncio
async def test_request_queued_behind_a_write_during_restart_goes_to_the_new_engine() -> None:
    """A request queued behind a large drain is not written to the dying engine."""
    new_engine = _PipedEngine()

    class Swapping(_PipedEngine):
        def __init__(self) -> None:
            super().__init__()
            self.restart_started = asyncio.Event()
            self.proceed = asyncio.Event()

        async def restart(self, env_overrides: dict[str, str]) -> None:
            self.restart_started.set()
            await self.proceed.wait()
            self._process = new_engine._process

    engine = Swapping()
    client = AttestClient(engine)  # type: ignore[arg-type]
    await client._write_lock.acquire()  # a drain in progress
    queued = asyncio.ensure_future(client.send_request("evaluate_batch", {}))
    await asyncio.sleep(0.01)
    restart = asyncio.ensure_future(client._restart_after_timeout(0, "evaluate_batch", 1.0))
    await engine.restart_started.wait()

    client._write_lock.release()
    await asyncio.sleep(0.05)
    written_to_dying_engine = list(engine.requests)

    engine.proceed.set()
    await restart
    result = await asyncio.wait_for(queued, timeout=2)

    assert written_to_dying_engine == []
    assert [r["method"] for r in new_engine.requests] == ["evaluate_batch"]
    assert result == {"data": "evaluate_batch"}
    await client.stop_reader()


_DRIFT_REPORT = {
    "report": {
        "assertion_id": "a1",
        "mean": 0.9,
        "stddev": 0.0,
        "count": 10,
        "latest_score": 0.7,
        "deviation": 0.0,
        "status": "ok",
    },
    "static_threshold": 0.6,
}


@pytest.mark.asyncio
async def test_query_drift_with_static_threshold_sends_it() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value=_DRIFT_REPORT)  # type: ignore[method-assign]

    await client.query_drift("a1", static_threshold=0.6)

    client.send_request.assert_awaited_once_with(
        "query_drift", {"assertion_id": "a1", "window_size": 50, "static_threshold": 0.6}
    )


@pytest.mark.asyncio
async def test_query_drift_without_static_threshold_omits_it() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value=_DRIFT_REPORT)  # type: ignore[method-assign]

    await client.query_drift("a1")

    client.send_request.assert_awaited_once_with(
        "query_drift", {"assertion_id": "a1", "window_size": 50}
    )


@pytest.mark.asyncio
async def test_query_drift_zero_static_threshold_is_sent() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value=_DRIFT_REPORT)  # type: ignore[method-assign]

    await client.query_drift("a1", static_threshold=0.0)

    assert client.send_request.await_args.args[1]["static_threshold"] == 0.0
