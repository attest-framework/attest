"""Tests for ``attest baseline`` and ``attest policy`` CLI shims.

The shims are pass-throughs to the engine binary, so unit tests focus on:

  - usage strings on no-arg / --help
  - engine-binary discovery via ATTEST_ENGINE_PATH
  - exit-code propagation
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from pathlib import Path
from unittest import mock

import pytest

from attest.cli.admin import _find_engine_binary, cmd_baseline, cmd_policy


@pytest.fixture
def stub_engine(tmp_path: Path) -> Iterator[Path]:
    """Yield a stub engine binary that records its argv to a sidecar file
    and exits 0.

    The CLI shim execs whatever ATTEST_ENGINE_PATH points to, so the
    stub is the simplest way to test the dispatch path without dragging
    in the Go binary.
    """
    log = tmp_path / "argv.log"
    binary = tmp_path / "fake-engine"
    binary.write_text(
        "#!/usr/bin/env bash\n"
        f'printf "%s\\n" "$@" > "{log}"\n'
        "exit ${ENGINE_EXIT:-0}\n"
    )
    binary.chmod(0o755)
    with mock.patch.dict(os.environ, {"ATTEST_ENGINE_PATH": str(binary)}):
        yield log


def test_baseline_no_args_prints_usage_and_returns_2(
    capsys: pytest.CaptureFixture[str],
) -> None:
    code = cmd_baseline([])
    captured = capsys.readouterr()
    assert code == 2
    assert "usage: attest baseline" in captured.err


def test_baseline_help_prints_usage_and_returns_2(
    capsys: pytest.CaptureFixture[str],
) -> None:
    code = cmd_baseline(["--help"])
    captured = capsys.readouterr()
    assert code == 2
    assert "usage: attest baseline" in captured.err


def test_policy_no_args_prints_usage_and_returns_2(
    capsys: pytest.CaptureFixture[str],
) -> None:
    code = cmd_policy([])
    captured = capsys.readouterr()
    assert code == 2
    assert "usage: attest policy" in captured.err


def test_baseline_passthrough_invokes_engine_with_subcommand(
    stub_engine: Path,
) -> None:
    code = cmd_baseline(["pin", "--tag", "v1", "--report", "/tmp/r.json"])
    assert code == 0
    argv = stub_engine.read_text().splitlines()
    assert argv == ["baseline", "pin", "--tag", "v1", "--report", "/tmp/r.json"]


def test_policy_passthrough_propagates_exit_code(
    stub_engine: Path,
) -> None:
    # ENGINE_EXIT=1 simulates a blocking policy violation.
    with mock.patch.dict(os.environ, {"ENGINE_EXIT": "1"}):
        code = cmd_policy(["evaluate", "--policy", "p.yaml", "--report", "r.json"])
    assert code == 1


def test_find_engine_binary_invalid_env_var_raises(tmp_path: Path) -> None:
    bogus = str(tmp_path / "does-not-exist")
    with mock.patch.dict(os.environ, {"ATTEST_ENGINE_PATH": bogus}):
        with pytest.raises(FileNotFoundError):
            _find_engine_binary()
