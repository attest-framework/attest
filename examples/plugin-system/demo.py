"""Plugin system demo — custom assertions via the AttestPlugin protocol.

Demonstrates how to create, register, and execute custom plugins
that extend Attest's assertion capabilities beyond the built-in layers.

Run:
    cd /path/to/attest
    PYTHONPATH=sdks/python/src python examples/plugin-system/demo.py
"""

from __future__ import annotations

from typing import Any

from attest import ManualAdapter, Trace
from attest.plugins import (
    AttestPlugin,
    PluginRegistry,
    PluginResult,
    load_entrypoint_plugins,
    register_plugin,
)


# -- Define custom plugins implementing the AttestPlugin protocol --


class ProfanityFilterPlugin:
    """Plugin that checks agent output for profanity."""

    name: str = "profanity-filter"
    plugin_type: str = "assertion"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        """Check output for banned words."""
        banned_words = spec.get("banned_words", ["damn", "hell"])
        output_text = (trace.output or {}).get("message", "").lower()

        violations = [w for w in banned_words if w in output_text]

        if violations:
            return PluginResult(
                status="fail",
                score=0.0,
                explanation=f"Found banned words: {', '.join(violations)}",
                metadata={"violations": violations},
            )

        return PluginResult(
            status="pass",
            score=1.0,
            explanation="No profanity detected",
        )


class ResponseLengthPlugin:
    """Plugin that validates response length is within bounds."""

    name: str = "response-length"
    plugin_type: str = "assertion"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        """Check output message length against min/max bounds."""
        min_chars = spec.get("min_chars", 10)
        max_chars = spec.get("max_chars", 5000)
        output_text = (trace.output or {}).get("message", "")
        length = len(output_text)

        if length < min_chars:
            return PluginResult(
                status="fail",
                score=length / min_chars,
                explanation=f"Response too short: {length} chars (min: {min_chars})",
            )

        if length > max_chars:
            return PluginResult(
                status="fail",
                score=max_chars / length,
                explanation=f"Response too long: {length} chars (max: {max_chars})",
            )

        # Score based on position within the ideal range
        normalized = (length - min_chars) / (max_chars - min_chars)
        return PluginResult(
            status="pass",
            score=1.0 - abs(normalized - 0.5),  # Best score at midpoint
            explanation=f"Response length {length} chars is within bounds [{min_chars}, {max_chars}]",
        )


class CostEfficiencyPlugin:
    """Plugin that rates cost efficiency based on output quality per dollar."""

    name: str = "cost-efficiency"
    plugin_type: str = "reporter"

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        """Calculate cost efficiency score."""
        cost = (trace.metadata.cost_usd if trace.metadata else None) or 0.0
        output_text = (trace.output or {}).get("message", "")
        output_length = len(output_text)

        if cost == 0:
            return PluginResult(
                status="pass",
                score=1.0,
                explanation="Zero cost — maximum efficiency",
            )

        # chars-per-dollar as efficiency metric
        efficiency = output_length / cost
        threshold = spec.get("min_chars_per_dollar", 10000)
        score = min(efficiency / threshold, 1.0)

        return PluginResult(
            status="pass" if score >= 0.7 else "fail",
            score=score,
            explanation=f"Efficiency: {efficiency:.0f} chars/$ (threshold: {threshold})",
            metadata={"chars_per_dollar": efficiency},
        )


def main() -> None:
    """Demonstrate plugin registration and execution."""
    # 1. Create a registry
    registry = PluginRegistry()

    # 2. Attempt to load any installed entry-point plugins
    ep_count = load_entrypoint_plugins(registry)
    print(f"Loaded {ep_count} entry-point plugin(s)")

    # 3. Register custom plugins explicitly
    profanity = ProfanityFilterPlugin()
    length = ResponseLengthPlugin()
    efficiency = CostEfficiencyPlugin()

    register_plugin(registry, profanity.name, profanity)
    register_plugin(registry, length.name, length)
    register_plugin(registry, efficiency.name, efficiency)

    # 4. List all registered plugins
    print(f"\nRegistered plugins: {registry.list_plugins()}")
    print(f"Assertion plugins: {registry.list_plugins('assertion')}")
    print(f"Reporter plugins: {registry.list_plugins('reporter')}")

    # 5. Build a sample trace
    adapter = ManualAdapter(agent_id="demo-agent")
    trace = adapter.capture(lambda b: (
        b.set_input(message="Tell me about AI testing"),
        b.add_llm_call(
            name="gpt-4.1",
            args={"model": "gpt-4.1"},
            result={"completion": "AI testing frameworks validate agent behavior."},
        ),
        b.set_output(message="AI testing frameworks validate agent behavior across multiple layers."),
        b.set_metadata(total_tokens=50, cost_usd=0.002),
    ))

    # 6. Execute each plugin
    print("\n--- Plugin Results ---")
    for plugin_name in ["profanity-filter", "response-length", "cost-efficiency"]:
        plugin_type = "assertion" if plugin_name != "cost-efficiency" else "reporter"
        plugin = registry.get(plugin_type, plugin_name)
        if plugin is None:
            print(f"  {plugin_name}: NOT FOUND")
            continue

        result = plugin.execute(trace, {
            "banned_words": ["damn", "crap"],
            "min_chars": 20,
            "max_chars": 500,
            "min_chars_per_dollar": 10000,
        })
        print(f"  {plugin_name}: {result.status} (score={result.score:.2f}) — {result.explanation}")


if __name__ == "__main__":
    main()
