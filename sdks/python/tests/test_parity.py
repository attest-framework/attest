from __future__ import annotations

import inspect
import os
from pathlib import Path
from typing import Any

import pytest

import attest
import attest.plugin as pytest_plugin
import attest.plugins as plugins_module
import attest.simulation as simulation_module
from attest.cli import admin
from attest.expect import ExpectChain
from tests.parity_matrix import (
    SECTIONS,
    UPDATE_ENV,
    Matrix,
    Surface,
    build_matrix,
    compare_surface,
    init_flags_from_source,
    load_json,
    render_matrix,
    serialize_surface,
    snake_to_camel,
)

REPO = Path(__file__).resolve().parents[3]
PARITY = REPO / "sdks" / "parity"
GUIDE = REPO / "docs" / "guides" / "parity.md"
PLANTED = Path(__file__).parent / "parity_fixtures" / "planted_gap"


def _public(names: list[str]) -> list[str]:
    return sorted(n for n in names if not n.startswith("_"))


def _is_fixture(obj: object) -> bool:
    return type(obj).__name__ == "FixtureFunctionDefinition" or hasattr(
        obj, "_pytestfixturefunction"
    )


class _MarkerRecorder:
    """Stands in for ``pytest.Config`` to read the markers the plugin registers."""

    def __init__(self) -> None:
        self.markers: list[str] = []

    def addinivalue_line(self, name: str, line: str) -> None:
        if name == "markers":
            self.markers.append(line.split("(")[0].split(":")[0].strip())

    def getoption(self, name: str, default: Any = None) -> Any:
        return default


def _adapter_names() -> list[str]:
    names: list[str] = []
    for name in attest.__all__:
        module = getattr(getattr(attest, name), "__module__", "")
        if module.startswith("attest.adapters.") and not module.endswith("._base"):
            names.append(name)
    return _public(names)


def _test_runner_names() -> list[str]:
    fixtures = [n for n, v in vars(pytest_plugin).items() if _is_fixture(v)]
    recorder = _MarkerRecorder()
    pytest_plugin.pytest_configure(recorder)  # type: ignore[arg-type]
    return _public(fixtures + recorder.markers)


def collect_surface() -> Surface:
    adapters = _adapter_names()
    return {
        "adapters": adapters,
        "exports": _public([n for n in attest.__all__ if n not in adapters]),
        "expect_chain": _public(dir(ExpectChain)),
        "init_flags": init_flags_from_source(inspect.getsource(admin._parse_init_args)),
        "plugins": _public(
            [
                n
                for n, v in vars(plugins_module).items()
                if getattr(v, "__module__", "") == plugins_module.__name__
            ]
        ),
        "simulation": _public(list(simulation_module.__all__)),
        "test_runner": _test_runner_names(),
    }


def _updating() -> bool:
    return os.environ.get(UPDATE_ENV) == "1"


def _load_surfaces() -> tuple[Surface, Surface]:
    return load_json(PARITY / "python.json"), load_json(PARITY / "typescript.json")


def _real_matrix() -> Matrix:
    python, typescript = _load_surfaces()
    return build_matrix(python, typescript, load_json(PARITY / "exceptions.json"))


def test_snake_to_camel_maps_names() -> None:
    assert snake_to_camel("output_has_all_keywords") == "outputHasAllKeywords"
    assert snake_to_camel("start_replay") == "startReplay"
    assert snake_to_camel("TIER_1") == "TIER_1"
    assert snake_to_camel("ExpectChain") == "ExpectChain"


def test_compare_surface_reports_added_and_removed_symbols() -> None:
    committed: Surface = {s: [] for s in SECTIONS}
    committed["simulation"] = ["scenario", "gone"]
    live: Surface = {s: [] for s in SECTIONS}
    live["simulation"] = ["scenario", "fresh"]

    assert compare_surface(committed, live) == ["added simulation.fresh", "removed simulation.gone"]


def test_python_snapshot_matches_live_surface() -> None:
    live = collect_surface()
    path = PARITY / "python.json"
    if _updating():
        PARITY.mkdir(parents=True, exist_ok=True)
        path.write_text(serialize_surface(live), encoding="utf-8")
    assert path.exists(), f"missing {path}; regenerate with {UPDATE_ENV}=1"
    drift = compare_surface(load_json(path), live)
    assert not drift, f"snapshot drift; regenerate with {UPDATE_ENV}=1 uv run pytest\n" + "\n".join(
        drift
    )


def test_parity_has_no_undocumented_gaps_or_stale_exceptions() -> None:
    matrix = _real_matrix()

    assert not matrix.problems, "\n".join(matrix.problems)


def test_parity_guide_matches_generated_matrix() -> None:
    rendered = render_matrix(_real_matrix())
    if _updating():
        GUIDE.write_text(rendered, encoding="utf-8")
    assert GUIDE.exists(), f"missing {GUIDE}; regenerate with {UPDATE_ENV}=1"
    assert GUIDE.read_text(encoding="utf-8") == rendered, (
        f"docs/guides/parity.md is stale; regenerate with {UPDATE_ENV}=1 uv run pytest"
    )


def test_parity_checker_reports_exactly_the_planted_gap() -> None:
    python = load_json(PLANTED / "python.json")
    typescript = load_json(PLANTED / "typescript.json")

    matrix = build_matrix(python, typescript, load_json(PLANTED / "exceptions.json"))

    assert matrix.problems == [
        "gap exports.planted_python_only: no TypeScript counterpart "
        "'plantedPythonOnly' and no documented exception"
    ]


def _exceptions(*entries: dict[str, str]) -> dict[str, Any]:
    return {"aliases": {}, "exceptions": list(entries)}


def _surface(**sections: list[str]) -> Surface:
    return {s: sections.get(s, []) for s in SECTIONS}


def test_parity_checker_flags_exception_for_name_python_no_longer_has() -> None:
    matrix = build_matrix(
        _surface(exports=["Agent"]),
        _surface(exports=["Agent"]),
        _exceptions({"python": "Gone", "reason": "upstream_python_only", "note": ""}),
    )

    assert matrix.problems == ["stale exception Gone: no such Python name"]


def test_parity_checker_flags_exception_once_typescript_has_the_counterpart() -> None:
    matrix = build_matrix(
        _surface(exports=["make_thing"]),
        _surface(exports=["makeThing"]),
        _exceptions({"python": "make_thing", "reason": "language_idiom", "note": "makeThing"}),
    )

    assert matrix.problems == ["stale exception make_thing: TypeScript now exports makeThing"]


def test_parity_checker_flags_outcome_capability_exception() -> None:
    matrix = build_matrix(
        _surface(simulation=["scenario"]),
        _surface(),
        _exceptions({"python": "scenario", "reason": "language_idiom", "note": "x"}),
    )

    assert "exception scenario: outcome capabilities cannot be exceptions" in matrix.problems


def test_parity_checker_flags_stale_alias() -> None:
    matrix = build_matrix(
        _surface(exports=["Persona"]),
        _surface(exports=["Other"]),
        {"aliases": {"Persona": "SimulatePersona"}, "exceptions": []},
    )

    assert "stale alias Persona: TypeScript does not export SimulatePersona" in matrix.problems


def test_parity_checker_rejects_unknown_reason() -> None:
    matrix = build_matrix(
        _surface(exports=["thing"]),
        _surface(),
        _exceptions({"python": "thing", "reason": "because", "note": ""}),
    )

    assert any("reason must be one of" in p for p in matrix.problems)


@pytest.mark.parametrize("section", SECTIONS)
def test_snapshots_define_every_section(section: str) -> None:
    python, typescript = _load_surfaces()

    assert section in python
    assert section in typescript
