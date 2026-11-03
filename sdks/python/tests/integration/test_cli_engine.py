"""`attest` CLI against the engine built by `make engine`: forwarding and version check."""

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
        Path(__file__).resolve().parents[4] / "proto" / "attest" / "v1" / "doctor.schema.json"
    ).read_text()
)

# Engine-spawning tests must not pick up a developer's judge configuration.
_JUDGE_KEYS = ("OPENAI_API_KEY", "ATTEST_JUDGE_API_KEY", "ATTEST_JUDGE_BASE_URL")


@pytest.fixture
def cli_env(tmp_path: Path) -> dict[str, str]:
    engine = _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    env = {k: v for k, v in os.environ.items() if k not in _JUDGE_KEYS}
    env.update(
        HOME=str(tmp_path / "home"),
        ATTEST_ENGINE_PATH=engine,
        ATTEST_ENGINE_NO_DOWNLOAD="1",
        ATTEST_CACHE_DIR=str(tmp_path / "cache"),
    )
    env.pop("ATTEST_SIMULATION", None)
    return env


def _attest(env: dict[str, str], *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, "-m", "attest", *args],
        capture_output=True,
        text=True,
        check=False,
        env=env,
    )


@pytest.mark.integration
def test_attest_doctor_json_forwards_engine_output(cli_env: dict[str, str]) -> None:
    proc = _attest(cli_env, "doctor", "--json")

    assert proc.returncode == 0, proc.stderr
    report = json.loads(proc.stdout)
    jsonschema.validate(report, _SCHEMA)
    assert report["judge"]["configured"] is False


@pytest.mark.integration
def test_attest_help_lists_engine_commands(cli_env: dict[str, str]) -> None:
    proc = _attest(cli_env, "--help")

    assert proc.returncode == 0, proc.stderr
    for command in ("init", "doctor", "policy", "baseline"):
        assert command in proc.stdout
