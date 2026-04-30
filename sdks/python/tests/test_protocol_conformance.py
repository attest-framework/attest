"""Cross-SDK protocol conformance suite, Python runner.

Each fixture in attest/protocol-tests/fixtures/ is replayed through the
real AttestClient line handler and validated against the declared
expected diagnostics and desync flag. The same fixtures power the
TypeScript SDK's conformance suite so both SDKs stay observationally
identical.
"""

from __future__ import annotations

import asyncio
import json
from collections.abc import Sequence
from pathlib import Path
from typing import Any

import pytest

from attest._proto.diagnostics import ProtocolDiagnostic
from attest.client import AttestClient

FIXTURES_DIR = Path(__file__).resolve().parents[3] / "protocol-tests" / "fixtures"


def _load_fixtures() -> list[dict[str, Any]]:
    if not FIXTURES_DIR.is_dir():
        raise FileNotFoundError(f"Conformance fixtures missing at {FIXTURES_DIR}")
    fixtures: list[dict[str, Any]] = []
    for path in sorted(FIXTURES_DIR.glob("*.json")):
        with path.open() as fh:
            fixture = json.load(fh)
        fixture["__path__"] = str(path)
        fixtures.append(fixture)
    return fixtures


def _ids(fixtures: Sequence[dict[str, Any]]) -> list[str]:
    return [f["name"] for f in fixtures]


FIXTURES = _load_fixtures()


@pytest.mark.parametrize("fixture", FIXTURES, ids=_ids(FIXTURES))
def test_protocol_conformance_fixture(fixture: dict[str, Any]) -> None:
    from unittest.mock import MagicMock

    engine = MagicMock()
    client = AttestClient(
        engine,
        protocol_logger=MagicMock(),
    )

    pending_ids: list[int] = list(fixture.get("prePending", []))
    futures: list[asyncio.Future[Any]] = []
    if pending_ids:
        loop = asyncio.new_event_loop()
        try:
            for req_id in pending_ids:
                fut: asyncio.Future[Any] = loop.create_future()
                client._pending[req_id] = fut
                futures.append(fut)
            for line in fixture["lines"]:
                client._handle_line(line.encode("utf-8") + b"\n")
        finally:
            for fut in futures:
                _drain_future(fut)
            for fut in client._pending.values():
                if not fut.done():
                    fut.cancel()
            loop.close()
    else:
        for line in fixture["lines"]:
            client._handle_line(line.encode("utf-8") + b"\n")

    diags = client.protocol_diagnostics()
    expected = fixture["expectedDiagnostics"]
    assert len(diags) == len(expected), (
        f"{fixture['name']}: expected {len(expected)} diagnostic(s), "
        f"got {len(diags)}: {[d.kind for d in diags]}"
    )
    for actual, want in zip(diags, expected, strict=True):
        _assert_diagnostic_matches(fixture["name"], actual, want)

    assert client._desynced is fixture["expectedDesync"], (
        f"{fixture['name']}: expected desynced={fixture['expectedDesync']}, "
        f"got {client._desynced}"
    )


def _drain_future(fut: asyncio.Future[Any]) -> None:
    """Consume any exception/result so asyncio doesn't warn about it."""
    if fut.done() and not fut.cancelled():
        try:
            fut.exception()
        except asyncio.InvalidStateError:
            pass


def _assert_diagnostic_matches(
    fixture_name: str,
    actual: ProtocolDiagnostic,
    want: dict[str, Any],
) -> None:
    assert actual.kind == want["kind"], (
        f"{fixture_name}: kind mismatch — expected {want['kind']}, got {actual.kind}"
    )
    needle = want.get("messageContains")
    if needle is not None:
        assert needle in actual.message, (
            f"{fixture_name}: expected message to contain '{needle}', "
            f"got '{actual.message}'"
        )
