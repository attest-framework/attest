"""The ``attest`` CLI: ``init`` runs locally, every other command forwards to a matching engine."""

from __future__ import annotations

import os
import subprocess
import sys
from importlib.metadata import entry_points
from pathlib import Path

import pytest

from attest import ENGINE_VERSION, __version__
from attest.cli.admin import main
from tests.conftest import ScriptedEngine


@pytest.fixture
def matching_engine(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine, monkeypatch: pytest.MonkeyPatch
) -> Path:
    engine = scripted_engine()
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    return engine


def test_main_help_with_engine_lists_sdk_and_engine_commands(
    matching_engine: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code = main(["--help"])

    out = capsys.readouterr().out
    assert code == 0
    assert "init" in out
    assert "doctor" in out
    assert "policy" in out


def test_main_no_args_prints_help(
    matching_engine: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert main([]) == 0
    assert "SDK commands" in capsys.readouterr().out


def test_main_help_without_engine_exits_1(
    isolated_engine_env: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code = main(["--help"])

    captured = capsys.readouterr()
    assert code == 1
    assert "init" in captured.out
    assert "ATTEST_ENGINE_NO_DOWNLOAD" in captured.err


def test_main_version_prints_sdk_and_required_engine(capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["--version"]) == 0
    out = capsys.readouterr().out
    assert __version__ in out
    assert ENGINE_VERSION in out


def test_main_init_scaffolds_without_engine(
    isolated_engine_env: Path, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    assert main(["init"]) == 0
    assert (project / "tests" / "conftest.py").is_file()


def test_main_policy_forwards_args_and_exit_code(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capfd: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(exit_code=2)))

    code = main(["policy", "evaluate", "--policy", "p.yaml", "--report", "r.json"])

    assert code == 2
    assert "forwarded: policy evaluate --policy p.yaml --report r.json" in capfd.readouterr().out


def test_main_unknown_command_is_forwarded_to_engine(
    matching_engine: Path, capfd: pytest.CaptureFixture[str]
) -> None:
    assert main(["doctor", "--json"]) == 0
    assert "forwarded: doctor --json" in capfd.readouterr().out


def test_main_policy_with_old_engine_exits_1_without_running_it(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capfd: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(version="0.8.0")))

    code = main(["policy", "evaluate", "--policy", "p.yaml", "--report", "r.json"])

    captured = capfd.readouterr()
    assert code == 1
    assert "forwarded" not in captured.out
    assert f"reports version 0.8.0; this SDK requires attest-engine {ENGINE_VERSION}" in (
        captured.err
    )


def test_main_policy_with_only_legacy_cache_engine_exits_1(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    capfd: pytest.CaptureFixture[str],
) -> None:
    scripted_engine(isolated_engine_env / ".attest" / "bin" / "attest-engine", "0.8.0")

    code = main(["policy", "evaluate", "--policy", "p.yaml", "--report", "r.json"])

    captured = capfd.readouterr()
    assert code == 1
    assert "forwarded" not in captured.out


def test_main_unknown_option_exits_2(capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["--bogus"]) == 2
    assert "unknown option --bogus" in capsys.readouterr().err


def test_console_script_attest_is_registered() -> None:
    (script,) = entry_points(group="console_scripts", name="attest")

    assert script.value == "attest.cli.admin:run"


def test_python_m_attest_help_exits_0(matching_engine: Path) -> None:
    proc = subprocess.run(
        [sys.executable, "-m", "attest", "--help"],
        capture_output=True,
        text=True,
        check=False,
        env={**os.environ},
    )

    assert proc.returncode == 0, proc.stderr
    assert "doctor" in proc.stdout


def test_main_init_with_extra_args_does_not_scaffold(
    isolated_engine_env: Path, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)

    assert main(["init", "--help"]) == 0
    assert main(["init", "somedir"]) == 2
    assert not (tmp_path / "tests").exists()


def test_main_help_subcommand_is_forwarded(
    matching_engine: Path, capfd: pytest.CaptureFixture[str]
) -> None:
    main(["help", "policy"])

    out = capfd.readouterr().out
    assert "commands:" in out  # the engine's own help output
    assert "SDK commands" not in out


def test_main_help_engine_without_commands_section_exits_1(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    from attest import ENGINE_VERSION

    engine = tmp_path / "bare" / "attest-engine"
    engine.parent.mkdir()
    engine.write_text(f"#!/bin/sh\necho 'attest-engine {ENGINE_VERSION}'\n")
    engine.chmod(0o755)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))

    assert main(["--help"]) == 1
    assert "no commands section" in capsys.readouterr().err
