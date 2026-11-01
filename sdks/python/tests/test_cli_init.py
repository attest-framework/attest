"""Tests for attest init scaffolding."""

from __future__ import annotations

from pathlib import Path

from attest.scaffold import scaffold_project


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
