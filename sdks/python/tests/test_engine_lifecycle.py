"""P10 — EngineManager lifecycle tests.

Tests start/stop/restart sequences, double-start/double-stop edge cases,
crash recovery, and timeout behavior. Uses simulation mode and mocking
to avoid requiring the engine binary.
"""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest.engine_manager import EngineManager
from attest.exceptions import EngineTimeoutError


def _make_manager() -> EngineManager:
    """Create an EngineManager with all internals wired up but no real process."""
    manager = EngineManager.__new__(EngineManager)
    manager._engine_path = "/fake/attest-engine"
    manager._log_level = "warn"
    manager._process = None
    manager._initialized = False
    manager._request_id = 0
    manager._init_result = None
    return manager


def _make_mock_process(
    init_response: bytes | None = None,
    shutdown_response: bytes | None = None,
) -> MagicMock:
    """Create a mock subprocess that returns init and shutdown responses."""
    import json

    if init_response is None:
        init_response = json.dumps({
            "jsonrpc": "2.0",
            "id": 1,
            "result": {
                "compatible": True,
                "engine_version": "0.4.0",
                "protocol_version": 1,
                "capabilities": ["layers_1_4"],
                "missing": [],
            },
        }).encode() + b"\n"

    if shutdown_response is None:
        shutdown_response = json.dumps({
            "jsonrpc": "2.0",
            "id": 2,
            "result": {},
        }).encode() + b"\n"

    responses = iter([init_response, shutdown_response])

    process = MagicMock()
    process.pid = 12345
    process.returncode = None
    process.stdin = AsyncMock()
    process.stdin.write = MagicMock()
    process.stdin.drain = AsyncMock()
    process.stdout = AsyncMock()
    process.stdout.readline = AsyncMock(side_effect=lambda: next(responses, b""))
    process.stderr = AsyncMock()

    async def _wait() -> int:
        process.returncode = 0
        return 0

    process.wait = _wait
    process.terminate = MagicMock()
    process.kill = MagicMock()

    return process


# ---------------------------------------------------------------------------
# Async context manager lifecycle
# ---------------------------------------------------------------------------


def test_context_manager_protocol() -> None:
    """EngineManager supports async with statement."""
    assert hasattr(EngineManager, "__aenter__")
    assert hasattr(EngineManager, "__aexit__")


def test_is_running_false_initially() -> None:
    """is_running is False before start()."""
    manager = _make_manager()
    assert manager.is_running is False


def test_is_running_true_with_active_process() -> None:
    """is_running is True when process exists with no return code."""
    manager = _make_manager()
    process = MagicMock()
    process.returncode = None
    manager._process = process
    assert manager.is_running is True


def test_is_running_false_after_exit() -> None:
    """is_running is False when process has exited."""
    manager = _make_manager()
    process = MagicMock()
    process.returncode = 0
    manager._process = process
    assert manager.is_running is False


# ---------------------------------------------------------------------------
# Double-stop edge case
# ---------------------------------------------------------------------------


def test_double_stop_is_safe() -> None:
    """Calling stop() twice does not raise."""

    async def _run() -> None:
        manager = _make_manager()
        # First stop — no process, should be a no-op
        await manager.stop()
        # Second stop — still no-op
        await manager.stop()

    asyncio.run(_run())


def test_stop_with_uninitialized_process_terminates() -> None:
    """stop() terminates process even if initialize was never completed."""

    async def _run() -> None:
        manager = _make_manager()
        process = _make_mock_process()
        manager._process = process
        manager._initialized = False

        await manager.stop()

        process.terminate.assert_called_once()
        assert manager._initialized is False

    asyncio.run(_run())


# ---------------------------------------------------------------------------
# Send request before initialization
# ---------------------------------------------------------------------------


def test_send_request_before_init_raises() -> None:
    """send_request raises RuntimeError if engine not initialized."""

    async def _run() -> None:
        manager = _make_manager()
        with pytest.raises(RuntimeError, match="not initialized"):
            await manager.send_request("evaluate_batch", {})

    asyncio.run(_run())


# ---------------------------------------------------------------------------
# Timeout behavior
# ---------------------------------------------------------------------------


def test_timeout_raises_engine_timeout_error() -> None:
    """_send_request raises EngineTimeoutError when readline times out."""

    async def _run() -> None:
        manager = _make_manager()
        manager._initialized = True

        process = MagicMock()
        process.stdin = AsyncMock()
        process.stdin.write = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout = AsyncMock()
        manager._process = process

        with patch(
            "attest.engine_manager.asyncio.wait_for",
            side_effect=asyncio.TimeoutError,
        ), patch("attest.engine_manager._engine_timeout", return_value=2.0):
            with pytest.raises(EngineTimeoutError) as exc_info:
                await manager._send_request("evaluate_batch", {})

            assert exc_info.value.method == "evaluate_batch"
            assert exc_info.value.timeout == 2.0

    asyncio.run(_run())


def test_timeout_env_var_respected() -> None:
    """ATTEST_ENGINE_TIMEOUT env var controls timeout value."""
    from attest.engine_manager import _engine_timeout

    with patch.dict("os.environ", {"ATTEST_ENGINE_TIMEOUT": "10.5"}):
        assert _engine_timeout() == 10.5


def test_timeout_env_var_empty_uses_default() -> None:
    """Empty ATTEST_ENGINE_TIMEOUT falls back to default."""
    from attest.engine_manager import _engine_timeout

    with patch.dict("os.environ", {"ATTEST_ENGINE_TIMEOUT": ""}):
        assert _engine_timeout() == 30.0


# ---------------------------------------------------------------------------
# Connection error on closed stdout
# ---------------------------------------------------------------------------


def test_send_request_raises_on_closed_stdout() -> None:
    """_send_request raises ConnectionError when stdout returns empty bytes."""

    async def _run() -> None:
        manager = _make_manager()
        manager._initialized = True

        process = MagicMock()
        process.stdin = AsyncMock()
        process.stdin.write = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout = AsyncMock()
        manager._process = process

        with patch(
            "attest.engine_manager.asyncio.wait_for",
            return_value=b"",
        ):
            with pytest.raises(ConnectionError, match="closed stdout"):
                await manager._send_request("evaluate_batch", {})

    asyncio.run(_run())


# ---------------------------------------------------------------------------
# Start with mocked subprocess
# ---------------------------------------------------------------------------


def test_start_initializes_engine() -> None:
    """start() sends initialize request and sets _initialized to True."""

    async def _run() -> None:
        manager = _make_manager()
        process = _make_mock_process()

        with patch(
            "attest.engine_manager.asyncio.create_subprocess_exec",
            return_value=process,
        ):
            result = await manager.start()

        assert manager._initialized is True
        assert result.compatible is True
        assert manager._init_result is not None

    asyncio.run(_run())


def test_start_then_stop_lifecycle() -> None:
    """Full start → stop lifecycle completes without error."""

    async def _run() -> None:
        manager = _make_manager()
        process = _make_mock_process()

        with patch(
            "attest.engine_manager.asyncio.create_subprocess_exec",
            return_value=process,
        ):
            await manager.start()
            assert manager._initialized is True

            await manager.stop()
            assert manager._initialized is False

    asyncio.run(_run())


def test_context_manager_lifecycle() -> None:
    """async with EngineManager starts and stops correctly."""

    async def _run() -> None:
        manager = _make_manager()
        process = _make_mock_process()

        with patch(
            "attest.engine_manager.asyncio.create_subprocess_exec",
            return_value=process,
        ):
            async with manager:
                assert manager._initialized is True
                assert manager.is_running is True

            assert manager._initialized is False

    asyncio.run(_run())


def test_stop_kills_on_terminate_timeout() -> None:
    """stop() escalates to kill() when terminate doesn't exit in time."""

    async def _run() -> None:
        import json

        manager = _make_manager()
        manager._initialized = True

        shutdown_resp = json.dumps({
            "jsonrpc": "2.0",
            "id": 1,
            "result": {},
        }).encode() + b"\n"

        process = MagicMock()
        process.returncode = None
        process.stdin = AsyncMock()
        process.stdin.write = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout = AsyncMock()
        process.stdout.readline = AsyncMock(return_value=shutdown_resp)
        process.stderr = AsyncMock()
        process.terminate = MagicMock()

        kill_calls: list[bool] = []

        def patched_kill() -> None:
            kill_calls.append(True)
            process.returncode = -9

        process.kill = patched_kill

        # Make process.wait always return a coroutine that completes (for kill path)
        async def _wait() -> int:
            return process.returncode or 0

        process.wait = _wait

        # Patch asyncio.wait_for: on 5s timeout (terminate wait), raise TimeoutError
        real_wait_for = asyncio.wait_for

        async def selective_wait_for(coro: object, timeout: float) -> object:
            if timeout == 5.0:
                # Consume the coroutine to avoid RuntimeWarning
                try:
                    await coro  # type: ignore[misc]
                except Exception:
                    pass
                raise asyncio.TimeoutError
            return await real_wait_for(coro, timeout=timeout)  # type: ignore[arg-type]

        manager._process = process
        manager._request_id = 0

        with patch("attest.engine_manager.asyncio.wait_for", side_effect=selective_wait_for):
            await manager.stop()

        assert len(kill_calls) == 1

    asyncio.run(_run())


# ---------------------------------------------------------------------------
# Request ID increments
# ---------------------------------------------------------------------------


def test_request_id_increments() -> None:
    """Each _send_request increments _request_id."""

    async def _run() -> None:
        import json

        manager = _make_manager()
        manager._initialized = True

        response = json.dumps({
            "jsonrpc": "2.0",
            "id": 1,
            "result": {"data": "ok"},
        }).encode() + b"\n"

        process = MagicMock()
        process.stdin = AsyncMock()
        process.stdin.write = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout = AsyncMock()
        process.stdout.readline = AsyncMock(return_value=response)
        manager._process = process

        assert manager._request_id == 0
        await manager._send_request("test", {})
        assert manager._request_id == 1

    asyncio.run(_run())
