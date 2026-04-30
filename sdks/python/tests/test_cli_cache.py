"""Tests for CLI cache subcommands (cache stats, cache clear)."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from unittest.mock import patch

import pytest


def _run_main(*args: str) -> tuple[str, int]:
    """Run attest CLI main() with given args, return (stdout, exit_code)."""
    import io

    from attest.__main__ import main

    captured_output = io.StringIO()
    exit_code = 0

    with patch.object(sys, "argv", ["attest", *args]):
        with patch("sys.stdout", captured_output):
            try:
                main()
            except SystemExit as e:
                exit_code = int(e.code) if e.code is not None else 0

    return captured_output.getvalue(), exit_code


class TestCacheStats:
    """Tests for `attest cache stats`."""

    def test_cache_stats_no_db(self, tmp_path: Path) -> None:
        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(output)
        assert data["exists"] is False
        assert data["file_size"] == 0

    def test_cache_stats_with_db(self, tmp_path: Path) -> None:
        db_file = tmp_path / "attest.db"
        db_file.write_bytes(b"fake sqlite data" * 10)

        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(output)
        assert data["exists"] is True
        assert data["file_size"] == db_file.stat().st_size
        assert data["path"] == str(db_file)

    def test_cache_stats_path_in_output(self, tmp_path: Path) -> None:
        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(output)
        assert "path" in data
        assert "attest.db" in data["path"]

    def test_cache_stats_default_dir(self) -> None:
        """Stats without ATTEST_CACHE_DIR uses ~/.attest/cache/attest.db."""
        env = {k: v for k, v in os.environ.items() if k != "ATTEST_CACHE_DIR"}
        with patch.dict(os.environ, env, clear=True):
            output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(output)
        assert ".attest" in data["path"]
        assert "attest.db" in data["path"]


class TestCacheClear:
    """Tests for `attest cache clear`."""

    def test_cache_clear_removes_db(self, tmp_path: Path) -> None:
        db_file = tmp_path / "attest.db"
        db_file.write_bytes(b"test data")
        assert db_file.exists()

        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "clear")

        assert code == 0
        assert not db_file.exists()

    def test_cache_clear_no_db(self, tmp_path: Path) -> None:
        """Clear when no DB exists prints message without error."""
        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "clear")

        assert code == 0
        assert "No cache to clear" in output

    def test_cache_clear_then_stats_shows_gone(self, tmp_path: Path) -> None:
        db_file = tmp_path / "attest.db"
        db_file.write_bytes(b"data")

        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            _run_main("cache", "clear")
            stats_output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(stats_output)
        assert data["exists"] is False

    def test_cache_clear_prints_path(self, tmp_path: Path) -> None:
        db_file = tmp_path / "attest.db"
        db_file.write_bytes(b"data")

        env = {**os.environ, "ATTEST_CACHE_DIR": str(tmp_path)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "clear")

        assert code == 0
        assert "attest.db" in output


class TestCacheEnvOverride:
    """Tests for ATTEST_CACHE_DIR environment variable."""

    def test_env_var_overrides_default_dir(self, tmp_path: Path) -> None:
        custom_dir = tmp_path / "custom_cache"
        custom_dir.mkdir()
        db_file = custom_dir / "attest.db"
        db_file.write_bytes(b"custom")

        env = {**os.environ, "ATTEST_CACHE_DIR": str(custom_dir)}
        with patch.dict(os.environ, env, clear=False):
            output, code = _run_main("cache", "stats")

        assert code == 0
        data = json.loads(output)
        assert data["exists"] is True
        assert str(custom_dir) in data["path"]


class TestCacheUnknownSubcommand:
    """Tests for unknown cache subcommands."""

    def test_unknown_subcommand_exits_nonzero(self) -> None:
        import io

        from attest.__main__ import main

        stderr_out = io.StringIO()
        with patch.object(sys, "argv", ["attest", "cache", "foobar"]):
            with patch("sys.stderr", stderr_out):
                with pytest.raises(SystemExit) as exc_info:
                    main()

        assert exc_info.value.code == 1
        assert "Unknown cache subcommand" in stderr_out.getvalue()
