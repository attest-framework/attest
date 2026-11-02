"""fixture-v1 (proto/attest/v1/fixture-v1.schema.json): cross-SDK fixtures.

The schema, which embeds trace-v2 by reference, accepts every
protocol-tests/fixture-v1/valid fixture and rejects every invalid one; the Go engine and
the TypeScript SDK run the same files. ``load_fixture`` reads every valid fixture and its
trace survives ``Trace.from_dict(...).to_dict()`` unchanged.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest
from referencing import Registry, Resource

from attest import CaptureError, load_fixture

_ROOT = Path(__file__).resolve().parents[3]
_FIXTURES = _ROOT / "protocol-tests" / "fixture-v1"
_SCHEMAS = _ROOT / "proto" / "attest" / "v1"

_VALID = sorted((_FIXTURES / "valid").glob("*.attest.json"))
_INVALID = sorted((_FIXTURES / "invalid").glob("*.attest.json"))


def _load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _validator() -> jsonschema.Draft202012Validator:
    trace = _load(_SCHEMAS / "trace-v2.schema.json")
    fixture = _load(_SCHEMAS / "fixture-v1.schema.json")
    registry: Registry[Any] = Registry().with_resources(
        [(s["$id"], Resource.from_contents(s)) for s in (trace, fixture)]
    )
    return jsonschema.Draft202012Validator(fixture, registry=registry)


def test_fixture_v1_fixtures_present() -> None:
    assert _VALID and _INVALID


@pytest.mark.parametrize("path", _VALID, ids=lambda p: p.name)
def test_fixture_v1_valid_fixture_matches_schema(path: Path) -> None:
    _validator().validate(_load(path))


@pytest.mark.parametrize("path", _INVALID, ids=lambda p: p.name)
def test_fixture_v1_invalid_fixture_is_rejected(path: Path) -> None:
    with pytest.raises(jsonschema.ValidationError):
        _validator().validate(_load(path))


@pytest.mark.parametrize("path", _VALID, ids=lambda p: p.name)
def test_load_fixture_valid_fixture_trace_round_trips(path: Path) -> None:
    doc = _load(path)

    fixture = load_fixture(path)

    assert fixture.name == doc["name"]
    assert fixture.trace.to_dict() == doc["trace"]
    assert [c.id for c in fixture.contracts] == [c["id"] for c in doc["contracts"]]
    assert fixture.replay_capable is doc["source"]["replay_capable"]


def test_load_fixture_incident_records_expected_failure() -> None:
    fixture = load_fixture(_FIXTURES / "valid" / "002-otlp-file-trace-only.attest.json")

    assert fixture.verified and fixture.expect_fail
    assert fixture.expected_failures == ["cardinality-issue_refund-per-order_id"]


def test_load_fixture_otlp_export_raises_capture_error(tmp_path: Path) -> None:
    path = tmp_path / "x.attest.json"
    path.write_text('{"resourceSpans": []}')

    with pytest.raises(CaptureError, match="not a fixture_version 1"):
        load_fixture(path)
