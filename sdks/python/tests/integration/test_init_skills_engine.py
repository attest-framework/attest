"""`attest init --skills --json` against the engine built by `make engine`."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from tests.integration.conftest import _engine_binary_path

_SCHEMA: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[4] / "proto" / "attest" / "v1" / "cli" / "init.schema.json"
    ).read_text(encoding="utf-8")
)


@pytest.fixture
def cli_env(tmp_path: Path) -> dict[str, str]:
    # ATTEST_ENGINE_PATH selects a branch-built engine; otherwise the `make engine` binary.
    engine = os.environ.get("ATTEST_ENGINE_PATH") or _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip(f"attest-engine binary not found at {engine}; build with 'make engine'")
    env = dict(os.environ)
    env.update(
        HOME=str(tmp_path / "home"),
        ATTEST_ENGINE_PATH=engine,
        ATTEST_ENGINE_NO_DOWNLOAD="1",
        ATTEST_CACHE_DIR=str(tmp_path / "cache"),
    )
    return env


def _init_json(env: dict[str, str], cwd: Path, *args: str) -> dict[str, Any]:
    proc = subprocess.run(
        [sys.executable, "-m", "attest", "init", *args, "--json"],
        cwd=cwd,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr
    doc: dict[str, Any] = json.loads(proc.stdout)
    jsonschema.Draft202012Validator(_SCHEMA).validate(doc)
    return doc


@pytest.mark.integration
def test_attest_init_skills_json_reports_skills_and_scaffold(
    cli_env: dict[str, str], tmp_path: Path
) -> None:
    project = tmp_path / "project"
    project.mkdir()

    doc = _init_json(cli_env, project, "--skills", "--ci", "github")

    assert {"tests/test_my_agent.py", "attest.policy.yaml", ".github/workflows/attest.yml"} <= set(
        doc["created"]
    )
    skills = set(doc["created"]) - {
        "tests/test_my_agent.py",
        "attest.policy.yaml",
        ".github/workflows/attest.yml",
    }
    assert skills, doc
    assert all((project / rel).is_file() for rel in doc["created"])
    assert doc["updated"] == []
    assert doc["skipped"] == []


@pytest.mark.integration
def test_attest_init_skills_json_rerun_skips_everything(
    cli_env: dict[str, str], tmp_path: Path
) -> None:
    project = tmp_path / "project"
    project.mkdir()
    first = _init_json(cli_env, project, "--skills")

    second = _init_json(cli_env, project, "--skills")

    assert second == {"created": [], "updated": [], "skipped": first["created"]}


@pytest.mark.integration
def test_attest_help_json_lists_init_and_validates_against_help_schema(
    cli_env: dict[str, str], tmp_path: Path
) -> None:
    schema = json.loads(
        (
            Path(__file__).resolve().parents[4]
            / "proto"
            / "attest"
            / "v1"
            / "cli"
            / "help.schema.json"
        ).read_text(encoding="utf-8")
    )
    proc = subprocess.run(
        [sys.executable, "-m", "attest", "help", "--json"],
        cwd=tmp_path,
        env=cli_env,
        capture_output=True,
        text=True,
        check=False,
    )

    assert proc.returncode == 0, proc.stderr
    doc = json.loads(proc.stdout)
    jsonschema.Draft202012Validator(schema).validate(doc)
    names = [c["name"] for c in doc["commands"]]
    assert "init" in names and "skills" in names
    init = next(c for c in doc["commands"] if c["name"] == "init")
    assert init["schema"] == "cli/init.schema.json"
