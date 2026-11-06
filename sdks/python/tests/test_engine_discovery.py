"""resolve_engine: the one version-checked discovery chain for fixtures and the CLI."""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from attest import ENGINE_VERSION
from attest.engine_manager import engine_version, resolve_engine
from attest.exceptions import EngineVersionMismatchError
from tests.conftest import ScriptedEngine

pytest_plugins = ["pytester"]


def _versioned_cache(home: Path) -> Path:
    return home / ".attest" / "bin" / ENGINE_VERSION / "attest-engine"


def test_engine_version_parses_version_output(scripted_engine: ScriptedEngine) -> None:
    engine = scripted_engine(version="9.9.9")

    assert engine_version(str(engine)) == "9.9.9"


def test_engine_version_silent_binary_returns_none(scripted_engine: ScriptedEngine) -> None:
    engine = scripted_engine(version="")

    assert engine_version(str(engine)) is None


def test_resolve_engine_env_path_matching_version_returns_it(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    engine = scripted_engine()
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))

    assert resolve_engine() == str(engine)


def test_resolve_engine_env_path_old_version_raises_mismatch(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    engine = scripted_engine(version="0.8.0")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))

    with pytest.raises(EngineVersionMismatchError) as excinfo:
        resolve_engine()

    assert excinfo.value.found == "0.8.0"
    assert excinfo.value.required == ENGINE_VERSION
    assert excinfo.value.source == "ATTEST_ENGINE_PATH"
    assert "reports version 0.8.0" in str(excinfo.value)


def test_resolve_engine_env_path_missing_file_raises(
    isolated_engine_env: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", "/nonexistent/attest-engine")

    with pytest.raises(FileNotFoundError, match="ATTEST_ENGINE_PATH"):
        resolve_engine()


def test_resolve_engine_explicit_path_wins_over_env(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    explicit = scripted_engine(tmp_path / "explicit" / "attest-engine")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(version="0.8.0")))

    assert resolve_engine(str(explicit)) == str(explicit)


def test_resolve_engine_explicit_path_mismatch_raises(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    engine = scripted_engine(version="0.9.0")

    with pytest.raises(EngineVersionMismatchError, match="explicit engine path"):
        resolve_engine(str(engine))


def test_resolve_engine_mismatched_engine_on_path_is_not_skipped(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine, tmp_path: Path
) -> None:
    scripted_engine(tmp_path / "path-bin" / "attest-engine", version="0.8.0")
    scripted_engine(_versioned_cache(isolated_engine_env))

    with pytest.raises(EngineVersionMismatchError, match="from PATH"):
        resolve_engine()


def test_resolve_engine_uses_versioned_cache_and_ignores_legacy_path(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    legacy = scripted_engine(isolated_engine_env / ".attest" / "bin" / "attest-engine", "0.8.0")
    legacy_bytes = legacy.read_bytes()
    cached = scripted_engine(_versioned_cache(isolated_engine_env))

    assert resolve_engine() == str(cached)
    assert legacy.read_bytes() == legacy_bytes


def test_resolve_engine_only_legacy_engine_and_no_download_raises(
    isolated_engine_env: Path, scripted_engine: ScriptedEngine
) -> None:
    scripted_engine(isolated_engine_env / ".attest" / "bin" / "attest-engine", "0.8.0")

    with pytest.raises(FileNotFoundError, match="ATTEST_ENGINE_NO_DOWNLOAD"):
        resolve_engine()


def test_resolve_engine_downloads_into_versioned_cache(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("ATTEST_ENGINE_NO_DOWNLOAD")
    target = _versioned_cache(isolated_engine_env)

    def download() -> Path:
        return scripted_engine(target)

    monkeypatch.setattr("attest.engine_manager.download_engine", download)

    assert resolve_engine() == str(target)


def test_resolve_engine_downloaded_engine_with_wrong_version_raises(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("ATTEST_ENGINE_NO_DOWNLOAD")
    target = _versioned_cache(isolated_engine_env)

    def download() -> Path:
        return scripted_engine(target, version="0.9.0")

    monkeypatch.setattr("attest.engine_manager.download_engine", download)

    with pytest.raises(EngineVersionMismatchError, match="from download"):
        resolve_engine()


def test_resolve_engine_empty_path_entry_does_not_search_cwd(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    workdir = tmp_path / "workdir"
    scripted_engine(workdir / "attest-engine")
    monkeypatch.chdir(workdir)
    monkeypatch.setenv("PATH", os.pathsep)

    with pytest.raises(FileNotFoundError, match="ATTEST_ENGINE_NO_DOWNLOAD"):
        resolve_engine()


def test_resolve_engine_path_ignores_non_executable_file(
    isolated_engine_env: Path, tmp_path: Path
) -> None:
    (tmp_path / "path-bin" / "attest-engine").write_text("not executable")

    with pytest.raises(FileNotFoundError):
        resolve_engine()


def test_resolve_engine_without_download_never_downloads(
    isolated_engine_env: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("ATTEST_ENGINE_NO_DOWNLOAD")

    def download() -> Path:
        raise AssertionError("must not download")

    monkeypatch.setattr("attest.engine_manager.download_engine", download)

    with pytest.raises(FileNotFoundError, match="No 'attest-engine'"):
        resolve_engine(allow_download=False)


def test_pytest_plugin_old_engine_fails_session_cleanly(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    pytester: pytest.Pytester,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(version="0.8.0")))
    pytester.makepyfile("def test_uses_engine(attest):\n    pass\n")

    result = pytester.runpytest_subprocess()

    result.assert_outcomes(errors=1)
    result.stdout.fnmatch_lines(["*attest-engine version mismatch*reports version 0.8.0*"])
    assert "Traceback" not in result.stdout.str()


def test_pytest_plugin_download_failure_fails_in_one_line(
    isolated_engine_env: Path,
    pytester: pytest.Pytester,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("ATTEST_ENGINE_NO_DOWNLOAD")
    monkeypatch.setenv("HTTPS_PROXY", "http://127.0.0.1:9")
    # An editable install has no hash table and refuses before any network use; pin one so the
    # run reaches the proxy.
    pytester.makeconftest(
        "import attest.engine_downloader as d\nd._embedded_hash = lambda plat: '0' * 64\n"
    )
    pytester.makepyfile("def test_uses_engine(attest):\n    pass\n")

    result = pytester.runpytest_subprocess()

    result.assert_outcomes(errors=1)
    result.stdout.fnmatch_lines(["*attest-engine download failed*ATTEST_ENGINE_PATH*"])
    assert "Traceback" not in result.stdout.str()
    # The download notice is printed outside pytest capture.
    assert "attest: downloading engine" in result.stderr.str() + result.stdout.str()
