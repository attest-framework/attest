"""Tests for AttestClient."""

from __future__ import annotations

import asyncio
import json
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from attest._proto.codec import encode_request
from attest._proto.types import Assertion, EvaluateBatchResult, Trace
from attest.client import AttestClient


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


@pytest.mark.asyncio
async def test_send_request_delegates_to_engine_without_reader() -> None:
    """send_request delegates to EngineManager when reader loop is not running."""
    engine = MagicMock()
    engine.send_request = AsyncMock(return_value={"accepted": True})
    client = AttestClient(engine)

    result = await client.send_request("test_method", {"key": "value"})

    engine.send_request.assert_awaited_once_with("test_method", {"key": "value"})
    assert result == {"accepted": True}


@pytest.mark.asyncio
async def test_send_request_id_correlation() -> None:
    """send_request assigns incrementing IDs and resolves futures by ID."""
    engine = MagicMock()

    # Simulate a process with stdin/stdout.
    # readline blocks until a future is waiting, then returns the response.
    # We use asyncio.Event to synchronize: reader blocks until send has
    # registered its future, then the response is returned.
    response1 = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"data": "first"}}).encode() + b"\n"
    response2 = json.dumps({"jsonrpc": "2.0", "id": 2, "result": {"data": "second"}}).encode() + b"\n"

    call_count = 0

    async def controlled_readline() -> bytes:
        nonlocal call_count
        call_count += 1
        # Yield control so send_request can register its future first
        await asyncio.sleep(0)
        if call_count == 1:
            return response1
        if call_count == 2:
            return response2
        return b""

    stdout_mock = MagicMock()
    stdout_mock.readline = controlled_readline

    stdin_mock = MagicMock()
    stdin_mock.write = MagicMock()
    stdin_mock.drain = AsyncMock()

    process_mock = MagicMock()
    process_mock.stdout = stdout_mock
    process_mock.stdin = stdin_mock

    engine._process = process_mock

    client = AttestClient(engine)
    client.start_reader()

    result1 = await client.send_request("method_a", {})
    result2 = await client.send_request("method_b", {})

    await client.stop_reader()

    assert result1 == {"data": "first"}
    assert result2 == {"data": "second"}
    assert client._request_id == 2


@pytest.mark.asyncio
async def test_submit_plugin_result_returns_accepted_flag() -> None:
    """submit_plugin_result extracts the 'accepted' bool from the response."""
    engine = MagicMock()
    engine.send_request = AsyncMock(return_value={"accepted": True})
    client = AttestClient(engine)

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
    engine = MagicMock()
    engine.send_request = AsyncMock(return_value={"accepted": False})
    client = AttestClient(engine)

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
    engine = MagicMock()
    engine.send_request = AsyncMock(
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
            "total_duration_ms": 10,
        }
    )
    client = AttestClient(engine)

    trace = Trace(trace_id="trc_1", output={"message": "hello"})
    assertions = [Assertion(assertion_id="a1", type="content", spec={"check": "contains", "value": "hello"})]

    batch_result = await client.evaluate_batch(trace, assertions)

    assert isinstance(batch_result, EvaluateBatchResult)
    assert len(batch_result.results) == 1
    assert batch_result.results[0].assertion_id == "a1"
    assert batch_result.results[0].status == "pass"

    call_args: Any = engine.send_request.call_args
    assert call_args[0][0] == "evaluate_batch"
    params = call_args[0][1]
    assert params["trace"]["trace_id"] == "trc_1"
    assert len(params["assertions"]) == 1


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
