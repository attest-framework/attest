"""Tests for client-side protocol diagnostics and desync detection."""

from __future__ import annotations

import asyncio
from typing import Any
from unittest.mock import MagicMock

import pytest

from attest._proto.diagnostics import (
    ProtocolDiagnostic,
    ProtocolDiagnosticBuffer,
    preview_line,
)
from attest.client import AttestClient
from attest.exceptions import ProtocolDesyncError


def _diag(message: str, ts: float) -> ProtocolDiagnostic:
    return ProtocolDiagnostic(
        kind="malformed_json",
        message=message,
        raw_line="",
        timestamp=ts,
    )


def test_buffer_rejects_non_positive_capacity() -> None:
    with pytest.raises(ValueError, match="positive"):
        ProtocolDiagnosticBuffer(0)
    with pytest.raises(ValueError, match="positive"):
        ProtocolDiagnosticBuffer(-1)


def test_buffer_retains_only_most_recent_up_to_capacity() -> None:
    buf = ProtocolDiagnosticBuffer(3)
    for i in range(5):
        buf.push(_diag(f"m{i}", float(i)))
    snap = buf.snapshot()
    assert [d.message for d in snap] == ["m2", "m3", "m4"]


def test_buffer_count_within_window() -> None:
    buf = ProtocolDiagnosticBuffer(10)
    base = 1_000.0
    for offset in (0.0, 0.5, 0.95, 0.999):
        buf.push(_diag(f"t{offset}", base + offset))

    now = base + 0.999
    assert buf.count_within(1.0, now) == 4
    assert buf.count_within(0.05, now) == 2
    assert buf.count_within(0.0, now) == 1


def test_preview_line_truncates_long_input() -> None:
    s = "x" * 1000
    assert len(preview_line(s)) == 513  # 512 + ellipsis char


def _make_client(**kwargs: Any) -> tuple[AttestClient, MagicMock]:
    engine = MagicMock()
    logger = MagicMock()
    client = AttestClient(engine, protocol_logger=logger, **kwargs)
    return client, logger


def test_handle_line_records_malformed_json() -> None:
    client, logger = _make_client()
    client._handle_line(b"{not json\n")

    diags = client.protocol_diagnostics()
    assert len(diags) == 1
    assert diags[0].kind == "malformed_json"
    logger.warning.assert_called_once()


def test_handle_line_records_missing_id() -> None:
    client, _ = _make_client()
    client._handle_line(b'{"jsonrpc":"2.0","result":{}}\n')

    diags = client.protocol_diagnostics()
    assert len(diags) == 1
    assert diags[0].kind == "missing_id"


def test_handle_line_records_unknown_id_when_no_pending() -> None:
    client, _ = _make_client()
    client._handle_line(b'{"jsonrpc":"2.0","id":42,"result":{}}\n')

    diags = client.protocol_diagnostics()
    assert len(diags) == 1
    assert diags[0].kind == "unknown_id"


def test_handle_line_records_non_routable_error() -> None:
    client, _ = _make_client()
    client._handle_line(
        b'{"jsonrpc":"2.0","id":99999,"error":{"code":3001,"message":"engine error"}}\n'
    )

    diags = client.protocol_diagnostics()
    assert len(diags) == 1
    assert diags[0].kind == "non_routable_error"


def test_diagnostic_listener_invoked() -> None:
    received: list[ProtocolDiagnostic] = []
    engine = MagicMock()
    client = AttestClient(
        engine,
        protocol_logger=MagicMock(),
        on_diagnostic=received.append,
    )

    client._handle_line(b"{not json\n")

    assert len(received) == 1
    assert received[0].kind == "malformed_json"


def test_unsubscribe_listener() -> None:
    received: list[ProtocolDiagnostic] = []
    engine = MagicMock()
    client = AttestClient(engine, protocol_logger=MagicMock())
    unsub = client.on_protocol_diagnostic(received.append)

    client._handle_line(b"{not json\n")
    assert len(received) == 1

    unsub()
    client._handle_line(b"{not json\n")
    assert len(received) == 1


@pytest.mark.asyncio
async def test_desync_triggers_after_threshold_breach() -> None:
    client, logger = _make_client(desync_threshold=3, desync_window_seconds=1.0)

    loop = asyncio.get_running_loop()
    fut: asyncio.Future[Any] = loop.create_future()
    client._pending[1] = fut

    for _ in range(3):
        client._handle_line(b"{not json\n")

    assert fut.done()
    exc = fut.exception()
    assert isinstance(exc, ProtocolDesyncError)
    assert len(exc.diagnostics) >= 3
    logger.error.assert_called()


@pytest.mark.asyncio
async def test_send_request_blocked_when_desynced() -> None:
    client, _ = _make_client(desync_threshold=1, desync_window_seconds=1.0)
    client._handle_line(b"{not json\n")

    with pytest.raises(ProtocolDesyncError):
        await client.send_request("noop", {})


def test_silent_logger_emits_nothing_without_debug_env(monkeypatch: Any) -> None:
    monkeypatch.delenv("ATTEST_DEBUG_PROTOCOL", raising=False)
    engine = MagicMock()
    client = AttestClient(engine)  # default logger
    client._handle_line(b"{not json\n")
    assert client.protocol_diagnostics()[0].kind == "malformed_json"
