"""Tests for EngineManager."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest.engine_manager import EngineManager, _engine_timeout, _find_engine_binary
from attest.exceptions import EngineTimeoutError


def test_find_engine_binary_not_found() -> None:
    """Raises FileNotFoundError with descriptive message when binary absent."""
    try:
        _find_engine_binary()
    except FileNotFoundError as e:
        assert "attest-engine" in str(e)
        assert "PATH" in str(e) or "bin" in str(e)


def test_engine_manager_not_initialized_raises() -> None:
    """send_request raises RuntimeError before start() is called."""
    import asyncio

    manager = EngineManager.__new__(EngineManager)
    manager._engine_path = "/nonexistent/attest-engine"
    manager._log_level = "warn"
    manager._process = None
    manager._initialized = False
    manager._request_id = 0
    manager._init_result = None

    async def _run() -> None:
        with pytest.raises(RuntimeError, match="not initialized"):
            await manager.send_request("evaluate_batch", {})

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


def test_send_request_raises_engine_timeout_error_on_timeout() -> None:
    """_send_request raises EngineTimeoutError when readline times out."""

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
                    await manager._send_request("evaluate_batch", {})
                assert exc_info.value.method == "evaluate_batch"
                assert exc_info.value.timeout == 5.0

    asyncio.run(_run())
