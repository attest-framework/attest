"""Attest global configuration."""

from __future__ import annotations

import os

_simulation_mode: bool = False
_sample_rate: float = 0.0
_alert_webhook: str | None = None
_alert_slack_url: str | None = None


def config(
    *,
    simulation: bool | None = None,
    sample_rate: float | None = None,
    alert_webhook: str | None = None,
    alert_slack_url: str | None = None,
) -> dict[str, bool | float | str | None]:
    """Configure Attest runtime behavior.

    Args:
        simulation: Enable simulation mode. When active, evaluate_batch
            returns deterministic results without spawning the engine.
            Can also be set via ATTEST_SIMULATION=1 environment variable.
        sample_rate: Fraction of traces to evaluate (0.0–1.0).
            Can also be set via ATTEST_SAMPLE_RATE environment variable.
        alert_webhook: Webhook URL for drift alerts.
            Can also be set via ATTEST_ALERT_WEBHOOK environment variable.
        alert_slack_url: Slack webhook URL for drift alerts.
            Can also be set via ATTEST_ALERT_SLACK_URL environment variable.

    Returns:
        Current configuration state.
    """
    global _simulation_mode  # noqa: PLW0603
    global _sample_rate  # noqa: PLW0603
    global _alert_webhook  # noqa: PLW0603
    global _alert_slack_url  # noqa: PLW0603

    if simulation is not None:
        _simulation_mode = simulation
    if sample_rate is not None:
        _sample_rate = sample_rate
    if alert_webhook is not None:
        _alert_webhook = alert_webhook
    if alert_slack_url is not None:
        _alert_slack_url = alert_slack_url

    return {
        "simulation": is_simulation_mode(),
        "sample_rate": get_sample_rate(),
        "alert_webhook": get_alert_webhook(),
        "alert_slack_url": get_alert_slack_url(),
    }


def is_simulation_mode() -> bool:
    """Check if simulation mode is active.

    Returns True if either:
    - attest.config(simulation=True) was called
    - ATTEST_SIMULATION=1 environment variable is set
    """
    if _simulation_mode:
        return True
    return os.environ.get("ATTEST_SIMULATION", "").strip() in ("1", "true", "yes")


def get_sample_rate() -> float:
    """Return the configured sample rate.

    Priority: config() call > ATTEST_SAMPLE_RATE env var > 0.0
    """
    if _sample_rate != 0.0:
        return _sample_rate
    env_val = os.environ.get("ATTEST_SAMPLE_RATE", "").strip()
    if env_val:
        return float(env_val)
    return 0.0


def get_alert_webhook() -> str | None:
    """Return the configured alert webhook URL.

    Priority: config() call > ATTEST_ALERT_WEBHOOK env var > None
    """
    if _alert_webhook is not None:
        return _alert_webhook
    return os.environ.get("ATTEST_ALERT_WEBHOOK") or None


def get_alert_slack_url() -> str | None:
    """Return the configured Slack alert webhook URL.

    Priority: config() call > ATTEST_ALERT_SLACK_URL env var > None
    """
    if _alert_slack_url is not None:
        return _alert_slack_url
    return os.environ.get("ATTEST_ALERT_SLACK_URL") or None


def reset() -> None:
    """Reset configuration to defaults. Intended for test teardown."""
    global _simulation_mode  # noqa: PLW0603
    global _sample_rate  # noqa: PLW0603
    global _alert_webhook  # noqa: PLW0603
    global _alert_slack_url  # noqa: PLW0603
    _simulation_mode = False
    _sample_rate = 0.0
    _alert_webhook = None
    _alert_slack_url = None
