"""Tests for the Attest plugin system."""

from __future__ import annotations

import asyncio
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from attest._proto.types import Trace
from attest.plugins import (
    AttestPlugin,
    PluginRegistry,
    PluginResult,
    execute_plugin_assertion,
    load_entrypoint_plugins,
    register_plugin,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


def _make_trace() -> Trace:
    return Trace(trace_id="t-001", output={"message": "hello"})


class _EchoPlugin:
    """Minimal plugin for testing — echoes score from spec."""

    name = "echo"
    plugin_type = "assertion"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        score = float(spec.get("score", 1.0))
        return PluginResult(status="pass", score=score, explanation="echo")


class _SlowPlugin:
    """Plugin that sleeps, used to test timeout behavior."""

    name = "slow"
    plugin_type = "assertion"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        import time

        time.sleep(10)
        return PluginResult(status="pass", score=1.0, explanation="never reached")


# ---------------------------------------------------------------------------
# PluginResult tests
# ---------------------------------------------------------------------------


class TestPluginResult:
    def test_required_fields(self) -> None:
        r = PluginResult(status="pass", score=0.9, explanation="looks good")
        assert r.status == "pass"
        assert r.score == 0.9
        assert r.explanation == "looks good"

    def test_metadata_defaults_to_none(self) -> None:
        r = PluginResult(status="fail", score=0.0, explanation="failed")
        assert r.metadata is None

    def test_metadata_can_be_set(self) -> None:
        r = PluginResult(status="pass", score=1.0, explanation="ok", metadata={"key": "val"})
        assert r.metadata == {"key": "val"}


# ---------------------------------------------------------------------------
# PluginRegistry tests
# ---------------------------------------------------------------------------


class TestPluginRegistry:
    def test_register_and_get(self) -> None:
        registry = PluginRegistry()
        plugin = _EchoPlugin()
        registry.register("echo", plugin)
        assert registry.get("assertion", "echo") is plugin

    def test_get_returns_none_for_unknown_type(self) -> None:
        registry = PluginRegistry()
        registry.register("echo", _EchoPlugin())
        assert registry.get("judge", "echo") is None

    def test_get_returns_none_for_unknown_name(self) -> None:
        registry = PluginRegistry()
        assert registry.get("assertion", "missing") is None

    def test_list_plugins_by_type(self) -> None:
        registry = PluginRegistry()
        registry.register("echo", _EchoPlugin())
        names = registry.list_plugins("assertion")
        assert "echo" in names

    def test_list_plugins_all(self) -> None:
        registry = PluginRegistry()
        registry.register("echo", _EchoPlugin())
        all_plugins = registry.list_plugins()
        assert "assertion/echo" in all_plugins

    def test_list_plugins_empty_type(self) -> None:
        registry = PluginRegistry()
        assert registry.list_plugins("judge") == []

    def test_register_plugin_helper(self) -> None:
        registry = PluginRegistry()
        plugin = _EchoPlugin()
        register_plugin(registry, "echo", plugin)
        assert registry.get("assertion", "echo") is plugin


# ---------------------------------------------------------------------------
# load_entrypoint_plugins tests
# ---------------------------------------------------------------------------


class TestLoadEntrypointPlugins:
    def test_returns_zero_when_no_plugins_installed(self) -> None:
        registry = PluginRegistry()
        count = load_entrypoint_plugins(registry)
        assert count == 0

    def test_registry_empty_after_no_plugins(self) -> None:
        registry = PluginRegistry()
        load_entrypoint_plugins(registry)
        assert registry.list_plugins() == []


# ---------------------------------------------------------------------------
# execute_plugin_assertion tests
# ---------------------------------------------------------------------------


class TestExecutePluginAssertion:
    def test_returns_plugin_result(self) -> None:
        plugin = _EchoPlugin()
        trace = _make_trace()
        client = MagicMock()
        client.submit_plugin_result = AsyncMock(return_value=True)

        result = asyncio.run(
            execute_plugin_assertion(
                plugin=plugin,
                trace=trace,
                spec={"score": 0.75},
                client=client,
                trace_id="t-001",
                assertion_id="a-001",
            )
        )

        assert isinstance(result, PluginResult)
        assert result.score == 0.75
        assert result.status == "pass"

    def test_submits_result_to_client(self) -> None:
        plugin = _EchoPlugin()
        trace = _make_trace()
        client = MagicMock()
        client.submit_plugin_result = AsyncMock(return_value=True)

        asyncio.run(
            execute_plugin_assertion(
                plugin=plugin,
                trace=trace,
                spec={"score": 1.0},
                client=client,
                trace_id="t-001",
                assertion_id="a-001",
            )
        )

        client.submit_plugin_result.assert_awaited_once_with(
            trace_id="t-001",
            plugin_name="echo",
            assertion_id="a-001",
            status="pass",
            score=1.0,
            explanation="echo",
        )

    def test_timeout_raises(self) -> None:
        plugin = _SlowPlugin()
        trace = _make_trace()
        client = MagicMock()
        client.submit_plugin_result = AsyncMock(return_value=True)

        with pytest.raises(asyncio.TimeoutError):
            asyncio.run(
                execute_plugin_assertion(
                    plugin=plugin,
                    trace=trace,
                    spec={},
                    client=client,
                    trace_id="t-001",
                    assertion_id="a-001",
                    timeout=0.05,
                )
            )
