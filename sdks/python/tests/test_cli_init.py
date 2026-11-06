"""Tests for attest init scaffolding."""

from __future__ import annotations

import importlib.util
import json
import re
import stat
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest.cli.admin import main
from attest.scaffold import ACTION_PINS, DEMO_MARKER, scaffold_project
from tests.conftest import ScriptedEngine

_REPO = Path(__file__).resolve().parents[3]
_spec = importlib.util.spec_from_file_location(
    "check_action_pins", _REPO / "scripts" / "check-action-pins.py"
)
assert _spec is not None and _spec.loader is not None
check_pins = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check_pins)
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
    attest_job, annotate_job = workflow.split("\n  annotate:\n")
    steps = attest_job.split("      - ")[1:]
    by_name = {s.split("\n")[0].removeprefix("name: "): s for s in steps}
    run_tests = by_name["Run tests"]
    assert "continue-on-error: true" in run_tests
    assert "pytest --attest-report=attest-report.json" in run_tests
    assert '# env:\n        #   ATTEST_REPORT_TRACES: "1"' in run_tests
    assert "\n        env:" not in run_tests
    assert "not redacted" in run_tests and "private repositories" in run_tests
    for name in (
        "Write the JUnit report",
        "Write the failure viewer",
        "Upload the Attest report",
        "Evaluate the merge policy",
    ):
        assert "if: always()" in by_name[name], name
    assert (
        "uv run attest report junit --report attest-report.json --out attest-junit.xml"
        in by_name["Write the JUnit report"]
    )
    assert (
        "uv run attest view attest-report.json --no-open --out attest-report.html"
        in by_name["Write the failure viewer"]
    )
    upload = by_name["Upload the Attest report"]
    assert "attest-report.json\n" in upload and "attest-junit.xml" in upload
    assert "attest-report.html" in upload
    assert "\npermissions:\n  contents: read\n\njobs:" in workflow
    assert attest_job.count("pull-requests") == 0
    assert "    permissions:\n      contents: read\n    steps:" in attest_job
    assert "    permissions:\n      contents: read\n      pull-requests: write\n" in annotate_job
    assert workflow.count("pull-requests: write") == 1
    assert '"$code" -eq 2' in by_name["Evaluate the merge policy"]
    assert "needs: attest" in annotate_job
    assert "always() && github.event_name == 'pull_request' &&" in annotate_job
    assert "github.event.pull_request.head.repo.full_name == github.repository" in annotate_job
    assert "pr-annotate@5d5b748b13da7ba034b3acfce631cee8fcae720c # v1.0.1" in annotate_job
    assert "name: attest-report" in annotate_job
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


@pytest.mark.parametrize(
    "args",
    [
        ["--ci"],
        ["--ci", "gitlab"],
        ["somedir"],
        ["--json", "--json"],
        ["--skills", "--skills"],
        ["--mcp", "--mcp"],
    ],
)
def test_main_init_bad_arguments_exit_2_without_scaffolding(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, args: list[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    assert main(["init", *args]) == 2

    assert not (tmp_path / "tests").exists()


_INIT_SCHEMA: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[3] / "proto" / "attest" / "v1" / "cli" / "init.schema.json"
    ).read_text(encoding="utf-8")
)


def _init_json(capsys: pytest.CaptureFixture[str], *args: str) -> dict[str, Any]:
    assert main(["init", *args, "--json"]) == 0
    out = capsys.readouterr().out
    doc: dict[str, Any] = json.loads(out)
    jsonschema.Draft202012Validator(_INIT_SCHEMA).validate(doc)
    return doc


def test_main_init_json_reports_created_files_as_the_only_stdout(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    doc = _init_json(capsys, "--ci", "github")

    assert doc == {
        "created": sorted([*_SAMPLE_FILES, _WORKFLOW]),
        "updated": [],
        "skipped": [],
    }
    assert (tmp_path / _WORKFLOW).is_file()


def test_main_init_json_reports_existing_files_as_skipped(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.chdir(tmp_path)
    (tmp_path / "attest.policy.yaml").write_text("# mine\n")

    doc = _init_json(capsys)

    assert doc == {
        "created": ["tests/test_my_agent.py"],
        "updated": [],
        "skipped": ["attest.policy.yaml"],
    }
    assert (tmp_path / "attest.policy.yaml").read_text() == "# mine\n"


def test_main_init_human_output_is_unchanged_without_json(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    assert main(["init"]) == 0

    out = capsys.readouterr().out
    assert f"Created: {tmp_path / 'tests' / 'test_my_agent.py'}" in out
    assert "Next: run `pytest`" in out


def _skills_engine(tmp_path: Path, skills_body: str) -> Path:
    """An attest-engine stand-in whose help lists ``skills`` and whose ``skills`` command
    runs ``skills_body`` (shell builtins only: PATH is isolated)."""
    from attest import ENGINE_VERSION

    target = tmp_path / "engine" / "attest-engine"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(
        "#!/bin/sh\n"
        'case "$1" in\n'
        f"  version) echo 'attest-engine {ENGINE_VERSION}' ;;\n"
        "  help) echo 'commands:'; echo '  skills  install agent skills' ;;\n"
        '  skills) echo "$*" > "$0.calls"; ' + skills_body + " ;;\n"
        "esac\n"
    )
    target.chmod(target.stat().st_mode | stat.S_IXUSR)
    return target


def _mcp_engine(tmp_path: Path, mcp_body: str) -> Path:
    """An attest-engine stand-in whose help lists ``mcp`` and whose ``mcp`` command runs
    ``mcp_body`` (shell builtins only: PATH is isolated)."""
    from attest import ENGINE_VERSION

    target = tmp_path / "engine" / "attest-engine"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(
        "#!/bin/sh\n"
        'case "$1" in\n'
        f"  version) echo 'attest-engine {ENGINE_VERSION}' ;;\n"
        "  help) echo 'commands:'; echo '  mcp  serve attest over MCP' ;;\n"
        '  mcp) echo "$*" > "$0.calls"; ' + mcp_body + " ;;\n"
        "esac\n"
    )
    target.chmod(target.stat().st_mode | stat.S_IXUSR)
    return target


_MCP_DOC = (
    'echo \'{"dir":"/x","created":[".mcp.json"],"updated":[".cursor/mcp.json"],'
    '"unchanged":[".codex/config.toml"],"conflict":[]}\''
)


@pytest.mark.parametrize(
    ("lockfile", "launch"),
    [(True, ["uv", "run", "attest", "mcp"]), (False, ["attest", "mcp"])],
    ids=["uv-project", "plain-project"],
)
def test_main_init_mcp_json_merges_engine_paths_and_passes_launch_command(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    lockfile: bool,
    launch: list[str],
) -> None:
    engine = _mcp_engine(tmp_path, _MCP_DOC)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    project = tmp_path / "project"
    project.mkdir()
    if lockfile:
        (project / "uv.lock").write_text("")
    monkeypatch.chdir(project)

    doc = _init_json(capsys, "--mcp")

    assert doc == {
        "created": sorted([*_SAMPLE_FILES, ".mcp.json"]),
        "updated": [".cursor/mcp.json"],
        "skipped": [".codex/config.toml"],
        "conflict": [],
    }
    calls = Path(f"{engine}.calls").read_text().split("\n", 1)[0]
    assert calls == f"mcp install --dir {project} --command {json.dumps(launch)} --json"


def test_main_init_mcp_conflict_has_its_own_label(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    engine = _mcp_engine(
        tmp_path,
        'echo \'{"dir":"/x","created":[],"updated":[],"unchanged":[],"conflict":[".mcp.json"]}\'',
    )
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    monkeypatch.chdir(tmp_path)

    doc = _init_json(capsys, "--mcp")

    assert doc["conflict"] == [".mcp.json"]
    assert ".mcp.json" not in doc["skipped"]


def test_main_init_mcp_conflict_text_is_not_printed_as_unchanged(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    engine = _mcp_engine(
        tmp_path,
        'echo \'{"dir":"/x","created":[],"updated":[],"unchanged":[],"conflict":[".mcp.json"]}\'',
    )
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    monkeypatch.chdir(tmp_path)

    assert main(["init", "--mcp"]) == 0

    err = capsys.readouterr().err
    assert "Conflict" in err
    assert "Skipped (unchanged): " + str(tmp_path / ".mcp.json") not in err


def test_main_init_without_mcp_has_no_conflict_key(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.chdir(tmp_path)

    assert "conflict" not in _init_json(capsys)


def test_main_init_mcp_engine_failure_exits_1_without_scaffolding(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(_mcp_engine(tmp_path, "echo boom >&2; exit 3")))
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    assert main(["init", "--mcp", "--json"]) == 1

    captured = capsys.readouterr()
    assert captured.out == ""
    assert "mcp install" in captured.err
    assert not (project / "tests").exists()


def test_main_init_skills_json_merges_engine_paths(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    engine = _skills_engine(
        tmp_path,
        'echo \'{"dir":"/x","created":[".claude/skills/attest/SKILL.md"],'
        '"updated":["AGENTS.md"],"unchanged":[".agents/skills/attest/SKILL.md"]}\'',
    )
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(engine))
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    doc = _init_json(capsys, "--skills", "--ci=github")

    assert doc == {
        "created": sorted([*_SAMPLE_FILES, _WORKFLOW, ".claude/skills/attest/SKILL.md"]),
        "updated": ["AGENTS.md"],
        "skipped": [".agents/skills/attest/SKILL.md"],
    }
    calls = Path(f"{engine}.calls").read_text().split()
    assert calls == ["skills", "install", "--dir", str(project), "--json"]


@pytest.mark.parametrize(
    "body",
    ["echo 'boom' >&2; exit 3", "echo 'not json'", "echo '{\"created\":[]}'"],
    ids=["exit-3", "not-json", "missing-keys"],
)
def test_main_init_skills_engine_failure_exits_1_without_scaffolding(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    body: str,
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(_skills_engine(tmp_path, body)))
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    assert main(["init", "--skills", "--json"]) == 1

    captured = capsys.readouterr()
    assert captured.out == ""
    assert "skills install" in captured.err
    assert not (project / "tests").exists()


def test_main_init_skills_with_engine_lacking_skills_exits_1(
    isolated_engine_env: Path,
    scripted_engine: ScriptedEngine,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(scripted_engine(commands=("doctor",))))
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    assert main(["init", "--skills"]) == 1

    assert "predates `attest skills`" in capsys.readouterr().err
    assert not (project / "tests").exists()


def test_main_init_skills_without_engine_exits_1(
    isolated_engine_env: Path,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    project = tmp_path / "project"
    project.mkdir()
    monkeypatch.chdir(project)

    assert main(["init", "--skills", "--json"]) == 1

    assert capsys.readouterr().out == ""
    assert not (project / "tests").exists()


def test_scaffold_project_ci_github_pins_every_action_by_sha(tmp_path: Path) -> None:
    scaffold_project(tmp_path, ci="github")

    assert check_pins.violations(tmp_path / _WORKFLOW) == []
    workflow = (tmp_path / _WORKFLOW).read_text()
    for action, (sha, version) in ACTION_PINS.items():
        if f"{action}@" in workflow:
            assert f"{action}@{sha} # {version}" in workflow


def test_scaffold_pin_checker_rejects_a_tag_pinned_action(tmp_path: Path) -> None:
    scaffold_project(tmp_path, ci="github")
    sha, version = ACTION_PINS["actions/checkout"]
    path = tmp_path / _WORKFLOW
    path.write_text(path.read_text().replace(f"@{sha} # {version}", "@v6"))

    (problem,) = check_pins.violations(path)
    assert "actions/checkout@v6" in problem


def test_scaffold_pins_equal_the_typescript_scaffold_pins() -> None:
    source = (_REPO / "sdks/typescript/packages/core/src/scaffold.ts").read_text()
    ts_pins = {
        m["action"]: (m["sha"], m["version"])
        for m in re.finditer(
            r'"(?P<action>[^"]+)": \{\s*sha: "(?P<sha>[0-9a-f]{40})",'
            r'\s*version: "(?P<version>[^"]+)"',
            source,
        )
    }
    shared = ts_pins.keys() & ACTION_PINS.keys()
    assert shared >= {"actions/checkout", "actions/upload-artifact", "actions/download-artifact"}
    assert "attest-framework/attest/.github/actions/pr-annotate" in shared
    for action in shared:
        assert ACTION_PINS[action] == ts_pins[action], action
