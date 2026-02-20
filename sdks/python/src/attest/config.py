"""Attest global configuration."""

from __future__ import annotations

import os

_simulation_mode: bool = False


def config(*, simulation: bool | None = None) -> dict[str, bool]:
    """Configure Attest runtime behavior.

    Args:
        simulation: Enable simulation mode. When active, evaluate_batch
            returns deterministic results without spawning the engine.
            Can also be set via ATTEST_SIMULATION=1 environment variable.

    Returns:
        Current configuration state.
    """
    global _simulation_mode  # noqa: PLW0603

    if simulation is not None:
        _simulation_mode = simulation

    return {"simulation": is_simulation_mode()}


def is_simulation_mode() -> bool:
    """Check if simulation mode is active.

    Returns True if either:
    - attest.config(simulation=True) was called
    - ATTEST_SIMULATION=1 environment variable is set
    """
    if _simulation_mode:
        return True
    return os.environ.get("ATTEST_SIMULATION", "").strip() in ("1", "true", "yes")


def reset() -> None:
    """Reset configuration to defaults. Intended for test teardown."""
    global _simulation_mode  # noqa: PLW0603
    _simulation_mode = False
