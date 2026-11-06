"""scripts/generate_claims.py: --check passes on generated output and fails on drift."""

from __future__ import annotations

import importlib.util
import json
import stat
import sys
from pathlib import Path
from types import ModuleType

import pytest

_ROOT = Path(__file__).resolve().parents[3]

_DOC = {
    "layers": [
        {
            "id": 1,
            "name": "Schema validation",
            "description": "Checks the output against a JSON Schema.",
            "assertion_types": ["schema"],
            "cost": "free",
            "deterministic": True,
        },
        {
            "id": 8,
            "name": "Plugins",
            "description": "Custom assertions.",
            "assertion_types": ["plugin"],
            "cost": "free",
            "deterministic": False,
        },
    ],
    "adapters": {
        "python": [{"name": "OpenAI", "module": "openai", "maturity": "stable"}],
        "typescript": [{"name": "OpenAI", "module": "openai", "maturity": "beta"}],
    },
    "features": [{"name": "Mutation testing", "maturity": "beta"}],
    "maturity_definitions": {"stable": "s", "beta": "b", "experimental": "e"},
}


def _load_script() -> ModuleType:
    spec = importlib.util.spec_from_file_location(
        "generate_claims", _ROOT / "scripts/generate_claims.py"
    )
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules["generate_claims"] = module
    spec.loader.exec_module(module)
    return module


def _regions(*names: str) -> str:
    return "".join(f"<!-- claims:{n}:start -->\n<!-- claims:{n}:end -->\n" for n in names)


@pytest.fixture
def repo(tmp_path: Path) -> tuple[Path, Path]:
    """A repository root holding the target files and an engine that prints a fixed document."""
    (tmp_path / "docs/guides").mkdir(parents=True)
    (tmp_path / "README.md").write_text("# Attest\n" + _regions("layers", "adapters", "features"))
    (tmp_path / "CONTRIBUTING.md").write_text("# Contributing\n")
    (tmp_path / "docs/guides/framework-adapters.md").write_text(_regions("adapter-tiers"))
    engine = tmp_path / "engine"
    engine.write_text(f"#!/bin/sh\ncat <<'JSON'\n{json.dumps(_DOC)}\nJSON\n")
    engine.chmod(engine.stat().st_mode | stat.S_IEXEC)
    return tmp_path, engine


def _run(root: Path, engine: Path, *flags: str) -> int:
    code: int = _load_script().main([*flags, "--root", str(root), "--engine", str(engine)])
    return code


def test_generate_then_check_passes(repo: tuple[Path, Path]) -> None:
    root, engine = repo

    assert _run(root, engine) == 0
    assert _run(root, engine, "--check") == 0
    assert "| plugins | Plugins |" in (root / "README.md").read_text()


def test_check_planted_go_sdk_claim_fails(repo: tuple[Path, Path]) -> None:
    root, engine = repo
    _run(root, engine)

    with (root / "README.md").open("a") as f:
        f.write("\nLanguage SDKs: Python, TypeScript, Go SDK.\n")

    assert _run(root, engine, "--check") == 1


def test_check_edited_generated_row_fails(repo: tuple[Path, Path]) -> None:
    root, engine = repo
    _run(root, engine)
    readme = root / "README.md"

    readme.write_text(readme.read_text().replace("(stable)", "(beta)"))

    assert _run(root, engine, "--check") == 1


@pytest.mark.parametrize(
    "claim",
    [
        "an 8-layer pipeline",
        "Layer 7: Simulation",
        "the L8 runner",
        "70% of tests",
        "decouples SDK and engine release cycles",
    ],
)
def test_check_retired_claim_fails(repo: tuple[Path, Path], claim: str) -> None:
    root, engine = repo
    _run(root, engine)

    with (root / "CONTRIBUTING.md").open("a") as f:
        f.write(f"\n{claim}\n")

    assert _run(root, engine, "--check") == 1


def test_check_missing_marker_exits(repo: tuple[Path, Path]) -> None:
    root, engine = repo
    (root / "README.md").write_text("# Attest\n")

    with pytest.raises(SystemExit):
        _run(root, engine, "--check")
