"""Tests for attest init and validate CLI commands."""

from __future__ import annotations

import os
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

from attest.scaffold import scaffold_project, validate_suite


def test_scaffold_project_creates_expected_files(tmp_path: Path) -> None:
    scaffold_project(tmp_path)

    tests_dir = tmp_path / "tests"
    assert tests_dir.is_dir()
    assert (tests_dir / "conftest.py").is_file()
    assert (tests_dir / "test_my_agent.py").is_file()


def test_scaffold_project_conftest_content(tmp_path: Path) -> None:
    scaffold_project(tmp_path)
    content = (tmp_path / "tests" / "conftest.py").read_text()
    assert "import attest" in content
    assert "@attest.agent" in content


def test_scaffold_project_sample_test_content(tmp_path: Path) -> None:
    scaffold_project(tmp_path)
    content = (tmp_path / "tests" / "test_my_agent.py").read_text()
    assert "from attest import expect" in content
    assert "output_contains" in content


def test_scaffold_project_does_not_overwrite_existing_conftest(tmp_path: Path) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    conftest = tests_dir / "conftest.py"
    original = "# my custom conftest\n"
    conftest.write_text(original)

    scaffold_project(tmp_path)

    assert conftest.read_text() == original


def test_scaffold_project_does_not_overwrite_existing_test_file(tmp_path: Path) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    sample_test = tests_dir / "test_my_agent.py"
    original = "# my custom test\n"
    sample_test.write_text(original)

    scaffold_project(tmp_path)

    assert sample_test.read_text() == original


def test_validate_suite_warns_on_missing_conftest(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    (tmp_path / "tests").mkdir()
    validate_suite(tmp_path)
    captured = capsys.readouterr()
    assert "conftest.py" in captured.err
    assert "Warning" in captured.err


def test_validate_suite_warns_on_no_test_files(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    (tests_dir / "conftest.py").write_text("# conftest\n")

    validate_suite(tmp_path)

    captured = capsys.readouterr()
    assert "Warning" in captured.err
    assert "test_*.py" in captured.err


def test_validate_suite_no_warnings_when_valid(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    (tests_dir / "conftest.py").write_text("# conftest\n")
    (tests_dir / "test_example.py").write_text("def test_x(): pass\n")

    validate_suite(tmp_path)

    captured = capsys.readouterr()
    assert "Warning" not in captured.err


def test_validate_suite_warns_on_stale_golden_traces(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    (tests_dir / "conftest.py").write_text("# conftest\n")
    (tests_dir / "test_example.py").write_text("def test_x(): pass\n")

    golden = tmp_path / "traces" / "my_agent.golden"
    golden.parent.mkdir()
    golden.write_text("trace data\n")

    stale_time = (datetime.now(tz=timezone.utc) - timedelta(days=31)).timestamp()
    os.utime(golden, (stale_time, stale_time))

    validate_suite(tmp_path)

    captured = capsys.readouterr()
    assert "golden" in captured.err
    assert "Warning" in captured.err


def test_validate_suite_no_warning_for_fresh_golden_traces(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    tests_dir = tmp_path / "tests"
    tests_dir.mkdir()
    (tests_dir / "conftest.py").write_text("# conftest\n")
    (tests_dir / "test_example.py").write_text("def test_x(): pass\n")

    golden = tmp_path / "traces" / "my_agent.golden"
    golden.parent.mkdir()
    golden.write_text("trace data\n")

    validate_suite(tmp_path)

    captured = capsys.readouterr()
    assert "Warning" not in captured.err
