"""Tests for attest init scaffolding."""

from __future__ import annotations

from pathlib import Path

import pytest

from attest import __version__
from attest.cli.admin import main
from attest.scaffold import DEMO_MARKER, scaffold_project

_SAMPLE_FILES = ("tests/test_my_agent.py", "attest.policy.yaml")
_WORKFLOW = ".github/workflows/attest.yml"


def test_scaffold_project_writes_sample_test_fixture_and_policy(tmp_path: Path) -> None:
    scaffold_project(tmp_path)

    for name in _SAMPLE_FILES:
        assert (tmp_path / name).is_file(), name
    assert not (tmp_path / _WORKFLOW).exists()


def test_scaffold_project_sample_test_defines_its_own_fixture(tmp_path: Path) -> None:
    scaffold_project(tmp_path)

    sample = (tmp_path / "tests" / "test_my_agent.py").read_text()
    assert "def my_agent_result(" in sample
    assert "my_agent_result: Trace" in sample
    assert "attest.evaluate(chain)" in sample
    assert not (tmp_path / "tests" / "conftest.py").exists()


def test_scaffold_project_marks_exactly_one_line_to_edit(tmp_path: Path) -> None:
    scaffold_project(tmp_path)

    sample = (tmp_path / "tests" / "test_my_agent.py").read_text().splitlines()
    marked = [line for line in sample if DEMO_MARKER in line]
    assert len(marked) == 1
    assert '.output_contains("refund")' in marked[0]


def test_scaffold_project_policy_blocks_hard_fails_and_warns_on_not_evaluated(
    tmp_path: Path,
) -> None:
    scaffold_project(tmp_path)

    policy = (tmp_path / "attest.policy.yaml").read_text()
    assert "max_hard_fails: 0" in policy
    assert "not_evaluated: warn" in policy


def test_scaffold_project_ci_github_writes_merge_gate_workflow(tmp_path: Path) -> None:
    scaffold_project(tmp_path, ci="github")

    workflow = (tmp_path / _WORKFLOW).read_text()
    steps = workflow.split("      - ")[1:]
    by_name = {s.split("\n")[0].removeprefix("name: "): s for s in steps}
    run_tests = by_name["Run tests"]
    assert "continue-on-error: true" in run_tests
    assert "pytest --attest-report=attest-report.json" in run_tests
    for name in ("Write the JUnit report", "Upload the Attest report", "Evaluate the merge policy"):
        assert "if: always()" in by_name[name], name
    assert (
        "uv run attest report junit --report attest-report.json --out attest-junit.xml"
        in by_name["Write the JUnit report"]
    )
    upload = by_name["Upload the Attest report"]
    assert "attest-report.json\n" in upload and "attest-junit.xml" in upload
    assert "\npermissions:\n  contents: read\n\njobs:" in workflow
    assert "    permissions:\n      contents: read\n      pull-requests: write\n" in workflow
    assert '"$code" -eq 2' in by_name["Evaluate the merge policy"]
    annotate = by_name["Annotate the pull request"]
    assert "always() && github.event_name == 'pull_request' &&" in annotate
    assert "github.event.pull_request.head.repo.full_name == github.repository" in annotate
    assert f"pr-annotate@v{__version__}" in annotate
    final = steps[-1]
    assert "if: always() && steps.tests.outcome == 'failure'" in final
    assert "exit 1" in final


def test_scaffold_project_unknown_ci_raises(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="unknown CI provider"):
        scaffold_project(tmp_path, ci="gitlab")


@pytest.mark.parametrize("name", [*_SAMPLE_FILES, _WORKFLOW])
def test_scaffold_project_does_not_overwrite_existing_file(tmp_path: Path, name: str) -> None:
    existing = tmp_path / name
    existing.parent.mkdir(parents=True, exist_ok=True)
    existing.write_text("# mine\n")

    scaffold_project(tmp_path, ci="github")

    assert existing.read_text() == "# mine\n"


@pytest.mark.parametrize("args", [["--ci", "github"], ["--ci=github"]])
def test_main_init_ci_github_writes_workflow(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, args: list[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    assert main(["init", *args]) == 0

    assert (tmp_path / _WORKFLOW).is_file()


@pytest.mark.parametrize("args", [["--ci"], ["--ci", "gitlab"], ["somedir"]])
def test_main_init_bad_arguments_exit_2_without_scaffolding(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, args: list[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    assert main(["init", *args]) == 2

    assert not (tmp_path / "tests").exists()
