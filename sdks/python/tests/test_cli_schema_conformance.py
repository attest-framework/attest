"""CLI ``--json`` documents (proto/attest/v1/cli/<stem>.schema.json) against their fixtures.

Every directory ``protocol-tests/cli/<stem>/`` pairs with ``cli/<stem>.schema.json``: each
``valid/*.json`` validates and each ``invalid/*.json`` is rejected. The Go engine and the
TypeScript SDK run the same files.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

_ROOT = Path(__file__).resolve().parents[3]
_FIXTURES = _ROOT / "protocol-tests" / "cli"
_SCHEMAS = _ROOT / "proto" / "attest" / "v1" / "cli"
_STEMS = sorted(d.name for d in _FIXTURES.iterdir() if d.is_dir())


def _validator(stem: str) -> jsonschema.Draft202012Validator:
    schema: dict[str, Any] = json.loads(
        (_SCHEMAS / f"{stem}.schema.json").read_text(encoding="utf-8")
    )
    jsonschema.Draft202012Validator.check_schema(schema)
    return jsonschema.Draft202012Validator(schema)


def _cases(kind: str) -> list[tuple[str, Path]]:
    return [
        (stem, path) for stem in _STEMS for path in sorted((_FIXTURES / stem / kind).glob("*.json"))
    ]


def _load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def test_cli_fixture_dirs_present() -> None:
    assert "init" in _STEMS


@pytest.mark.parametrize("stem", _STEMS)
def test_cli_stem_has_schema_and_valid_and_invalid_fixtures(stem: str) -> None:
    assert (_SCHEMAS / f"{stem}.schema.json").is_file()
    assert sorted((_FIXTURES / stem / "valid").glob("*.json")), f"{stem}: no valid fixture"
    assert sorted((_FIXTURES / stem / "invalid").glob("*.json")), f"{stem}: no invalid fixture"


@pytest.mark.parametrize(
    ("stem", "path"), _cases("valid"), ids=lambda v: v if isinstance(v, str) else v.name
)
def test_cli_valid_fixture_matches_schema(stem: str, path: Path) -> None:
    _validator(stem).validate(_load(path))


@pytest.mark.parametrize(
    ("stem", "path"), _cases("invalid"), ids=lambda v: v if isinstance(v, str) else v.name
)
def test_cli_invalid_fixture_is_rejected(stem: str, path: Path) -> None:
    assert not _validator(stem).is_valid(_load(path))
