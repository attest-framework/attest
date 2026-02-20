"""Tests for extended config fields: sample_rate, alert_webhook, alert_slack_url."""

from __future__ import annotations

import pytest

import attest
from attest.config import (
    config,
    get_alert_slack_url,
    get_alert_webhook,
    get_sample_rate,
    reset,
)


@pytest.fixture(autouse=True)
def _reset_config() -> None:
    """Reset config state before each test."""
    reset()
    yield
    reset()


class TestSampleRate:
    def test_default_sample_rate_is_zero(self) -> None:
        assert get_sample_rate() == 0.0

    def test_set_via_config(self) -> None:
        config(sample_rate=0.5)
        assert get_sample_rate() == 0.5

    def test_set_via_env_var(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_SAMPLE_RATE", "0.25")
        assert get_sample_rate() == 0.25

    def test_config_takes_precedence_over_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_SAMPLE_RATE", "0.1")
        config(sample_rate=0.9)
        assert get_sample_rate() == 0.9

    def test_reset_clears_sample_rate(self) -> None:
        config(sample_rate=0.75)
        reset()
        assert get_sample_rate() == 0.0


class TestAlertWebhook:
    def test_default_alert_webhook_is_none(self) -> None:
        assert get_alert_webhook() is None

    def test_set_via_config(self) -> None:
        config(alert_webhook="https://hooks.example.com/alert")
        assert get_alert_webhook() == "https://hooks.example.com/alert"

    def test_set_via_env_var(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_ALERT_WEBHOOK", "https://env.example.com/hook")
        assert get_alert_webhook() == "https://env.example.com/hook"

    def test_config_takes_precedence_over_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_ALERT_WEBHOOK", "https://env.example.com/hook")
        config(alert_webhook="https://code.example.com/hook")
        assert get_alert_webhook() == "https://code.example.com/hook"

    def test_reset_clears_alert_webhook(self) -> None:
        config(alert_webhook="https://hooks.example.com/alert")
        reset()
        assert get_alert_webhook() is None


class TestAlertSlackUrl:
    def test_default_alert_slack_url_is_none(self) -> None:
        assert get_alert_slack_url() is None

    def test_set_via_config(self) -> None:
        config(alert_slack_url="https://hooks.slack.com/services/XXX")
        assert get_alert_slack_url() == "https://hooks.slack.com/services/XXX"

    def test_set_via_env_var(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_ALERT_SLACK_URL", "https://hooks.slack.com/services/ENV")
        assert get_alert_slack_url() == "https://hooks.slack.com/services/ENV"

    def test_config_takes_precedence_over_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ATTEST_ALERT_SLACK_URL", "https://hooks.slack.com/services/ENV")
        config(alert_slack_url="https://hooks.slack.com/services/CODE")
        assert get_alert_slack_url() == "https://hooks.slack.com/services/CODE"

    def test_reset_clears_alert_slack_url(self) -> None:
        config(alert_slack_url="https://hooks.slack.com/services/XXX")
        reset()
        assert get_alert_slack_url() is None


class TestConfigReturnDict:
    def test_return_dict_includes_all_new_keys(self) -> None:
        result = config(
            sample_rate=0.5,
            alert_webhook="https://hooks.example.com/w",
            alert_slack_url="https://hooks.slack.com/s",
        )
        assert "sample_rate" in result
        assert "alert_webhook" in result
        assert "alert_slack_url" in result
        assert result["sample_rate"] == 0.5
        assert result["alert_webhook"] == "https://hooks.example.com/w"
        assert result["alert_slack_url"] == "https://hooks.slack.com/s"

    def test_return_dict_includes_simulation_key(self) -> None:
        result = config()
        assert "simulation" in result

    def test_reset_clears_all_values(self) -> None:
        config(
            sample_rate=1.0,
            alert_webhook="https://hooks.example.com/w",
            alert_slack_url="https://hooks.slack.com/s",
        )
        reset()
        assert get_sample_rate() == 0.0
        assert get_alert_webhook() is None
        assert get_alert_slack_url() is None
