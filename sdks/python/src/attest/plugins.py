"""Attest plugin system — Protocol, Registry, entrypoint discovery, execution."""

from __future__ import annotations

import asyncio
import importlib.metadata
import logging
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Protocol

from attest._proto.types import Trace

if TYPE_CHECKING:
    from attest.client import AttestClient

logger = logging.getLogger("attest.plugins")


class AttestPlugin(Protocol):
    """Protocol that all Attest plugins must implement."""

    name: str
    plugin_type: str  # "adapter", "assertion", "judge", "reporter"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        """Execute the plugin against a trace and return a result."""
        ...


@dataclass
class PluginResult:
    """Result returned by a plugin execution."""

    status: str
    score: float
    explanation: str
    metadata: dict[str, Any] | None = None


class PluginRegistry:
    """Stores registered plugins keyed by (plugin_type, name)."""

    def __init__(self) -> None:
        self._plugins: dict[str, dict[str, AttestPlugin]] = {}

    def register(self, name: str, plugin: AttestPlugin) -> None:
        """Register a plugin instance under its plugin_type and name."""
        plugin_type = plugin.plugin_type
        if plugin_type not in self._plugins:
            self._plugins[plugin_type] = {}
        self._plugins[plugin_type][name] = plugin

    def get(self, plugin_type: str, name: str) -> AttestPlugin | None:
        """Retrieve a registered plugin by type and name. Returns None if not found."""
        return self._plugins.get(plugin_type, {}).get(name)

    def list_plugins(self, plugin_type: str | None = None) -> list[str]:
        """List registered plugin names.

        If plugin_type is provided, list only plugins of that type.
        Otherwise list all plugins as "type/name" strings.
        """
        if plugin_type is not None:
            return list(self._plugins.get(plugin_type, {}).keys())
        result: list[str] = []
        for ptype, plugins in self._plugins.items():
            for pname in plugins:
                result.append(f"{ptype}/{pname}")
        return result


def load_entrypoint_plugins(registry: PluginRegistry) -> int:
    """Discover and load plugins from the 'attest.plugins' entry point group.

    Returns the number of plugins successfully loaded.
    """
    eps = importlib.metadata.entry_points(group="attest.plugins")
    loaded = 0
    for ep in eps:
        try:
            plugin: AttestPlugin = ep.load()
            registry.register(ep.name, plugin)
            loaded += 1
            logger.debug("Loaded plugin %r from entry point", ep.name)
        except Exception:
            logger.exception("Failed to load plugin %r from entry point", ep.name)
    return loaded


def register_plugin(registry: PluginRegistry, name: str, plugin: AttestPlugin) -> None:
    """Explicitly register a plugin instance into the registry."""
    registry.register(name, plugin)


async def execute_plugin_assertion(
    plugin: AttestPlugin,
    trace: Trace,
    spec: dict[str, Any],
    client: AttestClient,
    trace_id: str,
    assertion_id: str,
    timeout: float = 30.0,
) -> PluginResult:
    """Execute a plugin with a timeout and submit the result via client.

    Runs the synchronous plugin.execute() in a thread executor to avoid
    blocking the event loop. Submits the result to the engine via
    client.submit_plugin_result() after execution.

    Raises asyncio.TimeoutError if execution exceeds timeout seconds.
    """
    loop = asyncio.get_running_loop()

    result: PluginResult = await asyncio.wait_for(
        loop.run_in_executor(None, plugin.execute, trace, spec),
        timeout=timeout,
    )

    await client.submit_plugin_result(
        trace_id=trace_id,
        plugin_name=plugin.name,
        assertion_id=assertion_id,
        status=result.status,
        score=result.score,
        explanation=result.explanation,
    )

    return result
