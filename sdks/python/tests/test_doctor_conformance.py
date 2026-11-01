"""Cross-SDK doctor fixtures (protocol-tests/doctor) against doctor.schema.json."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

_ROOT = Path(__file__).resolve().parents[3]
_SCHEMA: dict[str, Any] = json.loads(
    (_ROOT / "proto" / "attest" / "v1" / "doctor.schema.json").read_text()
)
_VALID = sorted((_ROOT / "protocol-tests" / "doctor" / "valid").glob("*.json"))
_INVALID = sorted((_ROOT / "protocol-tests" / "doctor" / "invalid").glob("*.json"))


def test_doctor_fixtures_present() -> None:
    assert _VALID
    assert _INVALID


@pytest.mark.parametrize("path", _VALID, ids=lambda p: p.stem)
def test_doctor_valid_fixture_matches_schema(path: Path) -> None:
    jsonschema.validate(json.loads(path.read_text()), _SCHEMA)


@pytest.mark.parametrize("path", _INVALID, ids=lambda p: p.stem)
def test_doctor_invalid_fixture_is_rejected(path: Path) -> None:
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(json.loads(path.read_text()), _SCHEMA)
