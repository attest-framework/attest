"""Tests for EngineManager."""

from __future__ import annotations

import asyncio
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest.engine_manager import EngineManager, _engine_timeout
from attest.exceptions import EngineTimeoutError, EngineVersionMismatchError
from tests.conftest import ScriptedEngine


def test_engine_manager_old_engine_path_raises_mismatch(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    """An explicit engine path goes through the same version check as discovery."""
    with pytest.raises(EngineVersionMismatchError, match="0.8.0"):
        EngineManager(engine_path=str(scripted_engine(version="0.8.0")))


def test_engine_manager_no_engine_raises_file_not_found(isolated_engine_env: Path) -> None:
    with pytest.raises(FileNotFoundError, match="attest-engine"):
        EngineManager()


def test_engine_manager_start_initialize_version_mismatch_stops_and_raises(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    manager = EngineManager(engine_path=str(scripted_engine()))
    init = {
        "engine_version": "0.9.0",
        "protocol_version": 1,
        "capabilities": ["layers_1_4"],
        "missing": [],
        "compatible": True,
    }

    async def _run() -> None:
        with (
            patch("asyncio.create_subprocess_exec", AsyncMock(return_value=MagicMock(pid=1))),
            patch.object(manager, "_handshake", AsyncMock(return_value=init)),
            patch.object(manager, "stop", AsyncMock()) as stop,
            pytest.raises(EngineVersionMismatchError, match="from initialize"),
        ):
            await manager.start()
        stop.assert_awaited_once()

    asyncio.run(_run())


def test_engine_manager_start_initialize_error_stops_engine(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    manager = EngineManager(engine_path=str(scripted_engine()))

    async def _run() -> None:
        with (
            patch("asyncio.create_subprocess_exec", AsyncMock(return_value=MagicMock(pid=1))),
            patch.object(manager, "_handshake", AsyncMock(side_effect=ConnectionError("eof"))),
            patch.object(manager, "stop", AsyncMock()) as stop,
            pytest.raises(ConnectionError),
        ):
            await manager.start()
        stop.assert_awaited_once()

    asyncio.run(_run())


def test_engine_manager_is_running_false_when_no_process() -> None:
    """is_running returns False when no process has been started."""
    manager = EngineManager.__new__(EngineManager)
    manager._process = None
    assert manager.is_running is False


# Integration tests (require built engine binary) live in tests/integration/


# ---------------------------------------------------------------------------
# P6 — Engine read timeout
# ---------------------------------------------------------------------------


def test_engine_timeout_default() -> None:
    """_engine_timeout returns 30.0 when env var is unset."""
    with patch.dict("os.environ", {}, clear=False):
        import os

        os.environ.pop("ATTEST_ENGINE_TIMEOUT", None)
        assert _engine_timeout() == 30.0


def test_engine_timeout_from_env() -> None:
    """_engine_timeout reads ATTEST_ENGINE_TIMEOUT env var."""
    with patch.dict("os.environ", {"ATTEST_ENGINE_TIMEOUT": "60"}):
        assert _engine_timeout() == 60.0


def test_engine_timeout_invalid_env_uses_default() -> None:
    """_engine_timeout falls back to default on non-float env value."""
    with patch.dict("os.environ", {"ATTEST_ENGINE_TIMEOUT": "not-a-float"}):
        assert _engine_timeout() == 30.0


def test_engine_timeout_error_attributes() -> None:
    """EngineTimeoutError carries method and timeout attributes."""
    exc = EngineTimeoutError(method="evaluate_batch", timeout=30.0)
    assert exc.method == "evaluate_batch"
    assert exc.timeout == 30.0
    assert "evaluate_batch" in str(exc)
    assert "30" in str(exc)


def test_engine_timeout_error_is_importable_from_attest() -> None:
    """EngineTimeoutError is exported from the top-level attest package."""
    import attest

    assert hasattr(attest, "EngineTimeoutError")
    assert attest.EngineTimeoutError is EngineTimeoutError


def test_handshake_raises_engine_timeout_error_on_timeout() -> None:
    """_handshake raises EngineTimeoutError when readline times out."""

    async def _run() -> None:
        manager = EngineManager.__new__(EngineManager)
        manager._engine_path = "/nonexistent/attest-engine"
        manager._log_level = "warn"
        manager._request_id = 0
        manager._initialized = True
        manager._init_result = None

        # Mock subprocess with a stdin that accepts writes and a stdout that
        # hangs forever (simulated by wait_for raising TimeoutError).
        process = MagicMock()
        process.stdin = AsyncMock()
        process.stdin.write = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout = AsyncMock()
        manager._process = process

        with patch(
            "attest.engine_manager.asyncio.wait_for",
            side_effect=asyncio.TimeoutError,
        ):
            with patch("attest.engine_manager._engine_timeout", return_value=5.0):
                with pytest.raises(EngineTimeoutError) as exc_info:
                    await manager._handshake("evaluate_batch", {})
                assert exc_info.value.method == "evaluate_batch"
                assert exc_info.value.timeout == 5.0

    asyncio.run(_run())


def test_handshake_drops_lines_that_are_not_its_response() -> None:
    """Id-less lines and other ids are dropped; only the matching id is the response."""
    import json

    def line(**fields: object) -> bytes:
        return json.dumps({"jsonrpc": "2.0", **fields}).encode() + b"\n"

    async def _run() -> None:
        manager = EngineManager.__new__(EngineManager)
        manager._request_id = 0
        process = MagicMock()
        process.stdin = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout.readline = AsyncMock(
            side_effect=[
                line(method="drift_alert", params={}),
                line(id=99, result={"data": "someone else"}),
                b"not json\n",
                line(id=1, result={"data": "mine"}),
            ]
        )
        manager._process = process

        assert await manager._handshake("initialize", {}) == {"data": "mine"}

    asyncio.run(_run())


def test_subprocess_stream_limit_matches_the_engine_frame_cap() -> None:
    from attest.engine_manager import MAX_FRAME_BYTES

    assert MAX_FRAME_BYTES == 10 * 1024 * 1024


def _line(**fields: object) -> bytes:
    import json

    return json.dumps({"jsonrpc": "2.0", **fields}).encode() + b"\n"


def test_send_request_without_a_client_drops_id_less_lines() -> None:
    async def _run() -> None:
        manager = EngineManager.__new__(EngineManager)
        manager._request_id = 0
        manager._client = None
        process = MagicMock()
        process.stdin = MagicMock()
        process.stdin.drain = AsyncMock()
        process.stdout.readline = AsyncMock(
            side_effect=[
                _line(method="progress", params={}),
                _line(id=1, result={"data": "mine"}),
            ]
        )
        manager._process = process

        assert await manager.send_request("evaluate_batch", {}) == {"data": "mine"}

    asyncio.run(_run())


def test_send_request_with_an_attached_client_delegates_to_it() -> None:
    from attest.client import AttestClient

    async def _run() -> None:
        manager = EngineManager.__new__(EngineManager)
        manager._request_id = 0
        manager._client = None
        process = MagicMock()
        process.stdin = MagicMock()
        process.stdin.drain = AsyncMock()
        lines: asyncio.Queue[bytes] = asyncio.Queue()
        process.stdout.readline = lines.get
        manager._process = process
        client = AttestClient(manager)  # attaches itself
        lines.put_nowait(_line(method="progress", params={}))
        lines.put_nowait(_line(id=1, result={"data": "mine"}))
        manager._spawn_env = {}

        assert await manager.send_request("evaluate_batch", {}) == {"data": "mine"}
        assert process.stdin.write.call_count == 1  # one request, read by the client's reader
        await client.stop_reader()

    asyncio.run(_run())
