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


def test_main_help_without_engine_lists_sdk_commands_and_exits_0(
    isolated_engine_env: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code = main(["--help"])

    out = capsys.readouterr().out
    assert code == 0
    assert "init" in out
    assert "engine commands: not available" in out
    assert "ATTEST_ENGINE_PATH" in out


def test_main_help_never_downloads(
    isolated_engine_env: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("ATTEST_ENGINE_NO_DOWNLOAD")

    def no_download() -> Path:
        raise AssertionError("attest --help must not download the engine")

    monkeypatch.setattr("attest.engine_manager.download_engine", no_download)

    assert main(["--help"]) == 0
    assert main([]) == 0


def test_main_help_with_mismatched_engine_exits_0(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(version="0.8.0")))

    assert main(["--help"]) == 0
    assert "reports version 0.8.0" in capsys.readouterr().out


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
    assert (project / "tests" / "test_my_agent.py").is_file()


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


def test_main_help_engine_without_registry_exits_0(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(help_mode="server")))

    assert main(["--help"]) == 0
    assert "predates the command registry" in capsys.readouterr().out


def test_main_forward_to_engine_without_registry_exits_1_without_running(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capfd: pytest.CaptureFixture[str],
) -> None:
    """A same-version engine built before the registry (the v1.0.0-rc prerelease)."""
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(help_mode="server")))

    code = main(["doctor", "--json"])

    captured = capfd.readouterr()
    assert code == 1
    assert "forwarded" not in captured.out
    assert "predates `attest doctor`" in captured.err


def test_main_forward_command_missing_from_engine_exits_1_without_running(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capfd: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(commands=("policy",))))

    code = main(["doctor", "--json"])

    captured = capfd.readouterr()
    assert code == 1
    assert "forwarded" not in captured.out
    assert "predates `attest doctor`" in captured.err


def test_main_forward_engine_help_hang_times_out_and_exits_1(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
    capfd: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(help_mode="hang")))
    monkeypatch.setattr("attest.cli.admin._HELP_PROBE_TIMEOUT", 0.5)

    assert main(["doctor"]) == 1
    assert "predates `attest doctor`" in capfd.readouterr().err


def test_main_forward_engine_killed_by_signal_exits_128_plus_signal(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(signal="TERM")))

    assert main(["policy", "evaluate"]) == 128 + 15
