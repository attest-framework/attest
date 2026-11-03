"""Replay without an engine: mode resolution, summary parsing, the egress guard, the
report-v2 provenance the plugin writes and the terminal line."""

from __future__ import annotations

import json
import socket
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest import egress
from attest._proto.types import Usage
from attest.plugin import replay_line
from attest.replay import (
    ReplayError,
    ReplaySummary,
    live_line,
    resolve_mode,
)
from attest.report_v2 import TestRecord, build_report

_ROOT = Path(__file__).resolve().parents[3]
_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "report-v2.schema.json").read_text()
)
_MISS = (
    "replay miss: system prompt changed (hash a1b2c3d→e4f5a6b); "
    "re-record with `attest capture --refresh inc-1042`"
)


def _summary(**over: Any) -> ReplaySummary:
    doc: dict[str, Any] = {
        "fixture": "inc-1042",
        "mode": "strict",
        "replayed": 2,
        "live": 0,
        "misses": 0,
        "unused": 0,
        "live_cost_usd": 0.0,
        "unpriced_live_calls": 0,
        "records": [],
    }
    doc.update(over)
    return ReplaySummary.from_dict(doc)


def _missed(mode: str) -> ReplaySummary:
    return _summary(
        mode=mode,
        replayed=0,
        misses=1,
        records=[
            {
                "seq": 0,
                "provider": "openai",
                "api": "chat_completions",
                "outcome": "miss",
                "message": _MISS,
            }
        ],
    )


def test_resolve_mode_explicit_beats_env_and_ci(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CI", "true")
    monkeypatch.setenv("ATTEST_REPLAY", "off")

    assert resolve_mode("auto") == "auto"
    assert resolve_mode() == "off"


@pytest.mark.parametrize(
    ("ci", "want"), [("true", "strict"), ("", "auto"), ("false", "auto"), ("0", "auto")]
)
def test_resolve_mode_default_is_strict_in_ci_and_auto_locally(
    monkeypatch: pytest.MonkeyPatch, ci: str, want: str
) -> None:
    monkeypatch.delenv("ATTEST_REPLAY", raising=False)
    monkeypatch.setenv("CI", ci)

    assert resolve_mode() == want


def test_resolve_mode_unknown_value_raises(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("ATTEST_REPLAY", "sometimes")

    with pytest.raises(ReplayError, match="sometimes"):
        resolve_mode()


def test_live_line_zero_calls_is_the_documented_string() -> None:
    assert live_line(0, 0.0, 0) == "0 live model calls · $0.00"
    assert live_line(1, 0.0123, 0) == "1 live model call · $0.01"
    assert live_line(2, 0.5, 2) == "2 live model calls · $0.50 (+2 unpriced)"


def test_from_dict_missing_field_raises() -> None:
    with pytest.raises(ReplayError, match="unexpected replay summary"):
        ReplaySummary.from_dict({"fixture": "x"})


def test_first_miss_returns_the_message_of_the_first_missed_request() -> None:
    assert _missed("strict").first_miss == _MISS
    assert _summary().first_miss is None


def test_replay_line_sums_sessions_and_names_misses() -> None:
    line = replay_line({"a": [_summary()], "b": [_summary(replayed=1, live=1, live_cost_usd=0.5)]})

    assert line == "attest replay: 3 replayed · 1 live model call · $0.50"
    assert replay_line({}) is None
    assert replay_line({"a": [_missed("strict")]}) == (
        "attest replay: 0 replayed · 0 live model calls · $0.00 · 1 missed"
    )


def test_build_report_replayed_test_carries_provenance_and_validates() -> None:
    test = TestRecord(test_id="t.py::test_a", name="test_a", file="t.py", line=3)
    test.replay = [_summary()]

    report = build_report([test], simulated=False, usage=Usage())

    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert report["tests"][0]["replay"] == {
        "fixtures": ["inc-1042"],
        "mode": "strict",
        "replayed": 2,
        "live": 0,
        "misses": 0,
    }
    assert report["summary"]["replay"] == {
        "replayed": 2,
        "live": 0,
        "misses": 0,
        "live_cost_usd": 0.0,
        "unpriced_live_calls": 0,
    }
    assert report["summary"]["usage"]["model_calls"] == 0


def test_build_report_auto_miss_adds_replay_miss_row_and_reason() -> None:
    test = TestRecord(test_id="t.py::test_a", name="test_a", file="t.py", line=3, outcome="skipped")
    test.replay = [_missed("auto")]

    report = build_report([test], simulated=False, usage=Usage())

    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert report["summary"]["not_evaluated"] == 1
    assert report["results"][0]["reason"] == "replay_miss"
    assert report["results"][0]["explanation"] == _MISS
    assert report["tests"][0]["not_evaluated_reasons"] == ["replay_miss"]
    assert report["tests"][0]["replay"]["miss"] == _MISS


def test_build_report_strict_miss_adds_no_not_evaluated_row() -> None:
    test = TestRecord(test_id="t.py::test_a", name="test_a", file="t.py", line=3, outcome="failed")
    test.replay = [_missed("strict")]

    report = build_report([test], simulated=False, usage=Usage())

    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert report["summary"]["not_evaluated"] == 0
    assert report["tests"][0]["replay"]["misses"] == 1


def test_build_report_without_replay_has_no_replay_blocks() -> None:
    test = TestRecord(test_id="t.py::test_a", name="test_a", file="t.py", line=3)

    report = build_report([test], simulated=False, usage=Usage())

    assert "replay" not in report["summary"]
    assert "replay" not in report["tests"][0]


@pytest.fixture
def guard() -> Any:
    egress.install()
    yield
    egress.uninstall()


@pytest.mark.parametrize(
    "host",
    [
        "api.openai.com",
        "API.ANTHROPIC.COM.",
        "generativelanguage.googleapis.com",
        "x.openai.azure.com",
    ],
)
def test_egress_guard_provider_host_resolution_raises(guard: None, host: str) -> None:
    with pytest.raises(egress.EgressBlockedError, match="egress guard"):
        socket.getaddrinfo(host, 443)


def test_egress_guard_http_client_to_provider_fails_before_any_connection(guard: None) -> None:
    with pytest.raises(urllib.error.URLError) as info:
        urllib.request.urlopen("https://api.openai.com/v1/models", timeout=2)  # noqa: S310

    assert isinstance(info.value.reason, egress.EgressBlockedError)


def test_egress_guard_create_connection_to_provider_raises(guard: None) -> None:
    with pytest.raises(egress.EgressBlockedError):
        socket.create_connection(("api.anthropic.com", 443), timeout=2)


def test_egress_guard_allows_loopback_and_other_hosts(guard: None) -> None:
    assert socket.getaddrinfo("127.0.0.1", 80)
    assert socket.getaddrinfo("localhost", 80)
    assert not egress.is_provider_host("example.com")
    assert not egress.is_provider_host("openai.com.evil.example")


def test_egress_uninstall_restores_resolution() -> None:
    original = socket.getaddrinfo
    egress.install()
    egress.install()
    assert socket.getaddrinfo is not original

    egress.uninstall()
    egress.uninstall()

    assert socket.getaddrinfo is original


def test_egress_installs_are_counted_so_an_inner_release_keeps_the_outer_guard() -> None:
    original = socket.getaddrinfo
    egress.install()
    egress.install()

    egress.uninstall()
    with pytest.raises(egress.EgressBlockedError):
        socket.getaddrinfo("api.openai.com", 443)
    egress.uninstall()

    assert socket.getaddrinfo is original
