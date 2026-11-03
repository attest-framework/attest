"""The chain lines the engine's contract catalog renders (protocol-tests/contracts/rendered.json,
checked against the Go catalog by a drift test) applied to a real ``ExpectChain``."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from attest import expect, load_fixture

_ROOT = Path(__file__).resolve().parents[3]
_CASES: list[dict[str, Any]] = json.loads(
    (_ROOT / "protocol-tests" / "contracts" / "rendered.json").read_text(encoding="utf-8")
)["cases"]
# A fixture with a world snapshot, as ``FIXTURE`` is in a generated test.
_FIXTURE = load_fixture(
    _ROOT / "protocol-tests" / "fixture-v1" / "valid" / "005-world-snapshot.attest.json"
)


def test_rendered_lines_cover_every_catalog_method() -> None:
    assert len(_CASES) >= 13


@pytest.mark.parametrize("case", _CASES, ids=lambda c: str(c["statement"])[:60])
def test_rendered_python_line_applies_to_a_real_chain(case: dict[str, Any]) -> None:
    chain = expect(_FIXTURE.trace)
    before = len(chain.assertions)

    # Test-only eval of a line this repo's own catalog rendered: the line is pasted into a
    # chain exactly as a generated test does.
    result = eval(  # noqa: S307
        "chain" + case["python"], {"chain": chain, "FIXTURE": _FIXTURE}
    )

    assert len(result.assertions) == before + 1
    recorded = result.assertions[-1]
    spec = dict(recorded.spec)
    if recorded.type == "effect":
        # An effect spec embeds the world it checks; it must be the fixture's.
        assert spec.pop("world") == _FIXTURE.world
    assert recorded.type == case["type"]
    assert spec == case["spec"]
    if case["chain_prefix"]:
        assert case["python"].startswith(case["chain_prefix"])
