"""world-v1 (proto/attest/v1/world-v1.schema.json): world definitions and snapshots.

The schema accepts every protocol-tests/world-v1 valid definition and snapshot and rejects
every invalid one; the Go engine and the TypeScript SDK run the same files. Snapshots
validate through the schema's ``snapshot`` definition.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

_ROOT = Path(__file__).resolve().parents[3]
_FIXTURES = _ROOT / "protocol-tests" / "world-v1"
_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "world-v1.schema.json").read_text(encoding="utf-8")
)
_DEFINITION = jsonschema.Draft202012Validator(_SCHEMA)
_SNAPSHOT = jsonschema.Draft202012Validator({"$ref": "#/$defs/snapshot", "$defs": _SCHEMA["$defs"]})


def _files(kind: str) -> list[Path]:
    return sorted((_FIXTURES / kind).glob("*.json"))


def test_world_v1_fixtures_present() -> None:
    assert all(_files(k) for k in ("valid", "invalid", "snapshot-valid", "snapshot-invalid"))


@pytest.mark.parametrize("path", _files("valid"), ids=lambda p: p.name)
def test_world_v1_definition_valid_is_accepted(path: Path) -> None:
    _DEFINITION.validate(json.loads(path.read_text(encoding="utf-8")))


@pytest.mark.parametrize("path", _files("invalid"), ids=lambda p: p.name)
def test_world_v1_definition_invalid_is_rejected(path: Path) -> None:
    assert not _DEFINITION.is_valid(json.loads(path.read_text(encoding="utf-8")))


@pytest.mark.parametrize("path", _files("snapshot-valid"), ids=lambda p: p.name)
def test_world_v1_snapshot_valid_is_accepted(path: Path) -> None:
    _SNAPSHOT.validate(json.loads(path.read_text(encoding="utf-8")))


@pytest.mark.parametrize("path", _files("snapshot-invalid"), ids=lambda p: p.name)
def test_world_v1_snapshot_invalid_is_rejected(path: Path) -> None:
    assert not _SNAPSHOT.is_valid(json.loads(path.read_text(encoding="utf-8")))
