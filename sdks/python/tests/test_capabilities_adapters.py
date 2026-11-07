"""The adapter list the engine publishes in ``attest-engine capabilities`` matches the SDK.

``engine/internal/capabilities/data.json`` is the single source of the adapter lists in the
README and the guides. A file added to or removed from ``attest/adapters`` fails here until
the source lists it with a maturity tier.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

_ROOT = Path(__file__).resolve().parents[3]
_DATA = _ROOT / "engine" / "internal" / "capabilities" / "data.json"
_ADAPTERS = _ROOT / "sdks" / "python" / "src" / "attest" / "adapters"
_TIERS = {"stable", "beta", "experimental"}


def _listed() -> list[dict[str, Any]]:
    data = json.loads(_DATA.read_text(encoding="utf-8"))
    adapters: list[dict[str, Any]] = data["adapters"]["python"]
    return adapters


def test_capabilities_python_adapters_match_adapter_modules() -> None:
    on_disk = sorted(p.stem for p in _ADAPTERS.glob("*.py") if not p.stem.startswith("_"))

    listed = sorted(a["module"] for a in _listed())

    assert listed == on_disk


def test_capabilities_python_adapters_have_known_maturity() -> None:
    for adapter in _listed():
        assert adapter["maturity"] in _TIERS, adapter
