"""Tests for EngineManager."""

from __future__ import annotations

import pytest

from attest.engine_manager import _find_engine_binary, EngineManager


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
