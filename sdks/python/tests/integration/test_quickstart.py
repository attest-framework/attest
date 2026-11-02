"""The quickstart as a user runs it: the project `attest init` generates, and every
README code block.

Runs against the engine built by `make engine`, in a fresh directory with HOME
redirected and no provider keys.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest.scaffold import DEMO_MARKER
from tests.integration.conftest import _engine_binary_path

_REPO = Path(__file__).resolve().parents[4]
_README = _REPO / "README.md"
_BROKEN_README = Path(__file__).parent / "data" / "readme_broken_snippet.md"
_REPORT_SCHEMA: dict[str, Any] = json.loads(
    (_REPO / "proto" / "attest" / "v1" / "report-v2.schema.json").read_text()
)
_PROVIDER_KEYS = (
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "ATTEST_GEMINI_API_KEY",
    "ATTEST_JUDGE_API_KEY",
    "ATTEST_JUDGE_BASE_URL",
    "ATTEST_JUDGE_PROVIDER",
)


@pytest.fixture
def project(tmp_path: Path) -> Path:
    engine = _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    path = tmp_path / "project"
    path.mkdir()
    return path


def _env(project: Path) -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if k not in _PROVIDER_KEYS}
    env.update(
        HOME=str(project.parent / "home"),
        ATTEST_ENGINE_PATH=_engine_binary_path(),
        ATTEST_ENGINE_NO_DOWNLOAD="1",
        ATTEST_CACHE_DIR=str(project.parent / "cache"),
    )
    for key in ("ATTEST_SIMULATION", "ATTEST_STRICT", "PYTEST_ADDOPTS"):
        env.pop(key, None)
    return env


def _run(project: Path, *argv: str) -> subprocess.CompletedProcess[str]:
    """Run ``uv run <argv>`` the way the quickstart does, with this interpreter."""
    module = {"pytest": "pytest", "attest": "attest"}[argv[0]]
    return subprocess.run(
        [sys.executable, "-m", module, *argv[1:]],
        cwd=project,
        env=_env(project),
        capture_output=True,
        text=True,
        check=False,
    )


# --- generated project -------------------------------------------------------


def _edit_marked_line(project: Path, old: str, new: str) -> None:
    test_file = project / "tests" / "test_my_agent.py"
    lines = test_file.read_text().splitlines(keepends=True)
    (index,) = [i for i, line in enumerate(lines) if DEMO_MARKER in line]
    lines[index] = lines[index].replace(old, new, 1)
    test_file.write_text("".join(lines))


@pytest.mark.integration
def test_generated_project_passes_then_fails_with_diagnostic_and_gates_merge(
    project: Path,
) -> None:
    assert _run(project, "attest", "init").returncode == 0

    green = _run(project, "pytest")
    assert green.returncode == 0, green.stdout + green.stderr
    assert "1 passed" in green.stdout

    _edit_marked_line(project, '"refund"', '"store credit"')
    red = _run(project, "pytest", "--attest-report=r.json")
    assert red.returncode == 1, red.stdout + red.stderr
    assert "1 failed" in red.stdout
    assert "L4 content" in red.stdout
    assert 'expected:   contains "store credit"' in red.stdout
    assert "actual:     Your refund of $42.00 has been issued." in red.stdout

    report = json.loads((project / "r.json").read_text())
    jsonschema.validate(report, _REPORT_SCHEMA, format_checker=jsonschema.FormatChecker())
    source = (project / "tests" / "test_my_agent.py").read_text().splitlines()
    def_line = next(i for i, line in enumerate(source, 1) if line.startswith("def test_my_agent"))
    (failing,) = [r for r in report["results"] if r["status"] == "hard_fail"]
    assert (failing["file"], failing["line"]) == ("tests/test_my_agent.py", def_line)
    assert report["tests"][0]["outcome"] == "failed"

    policy_args = ("--report", "r.json", "--policy", "attest.policy.yaml")
    assert _run(project, "attest", "policy", "evaluate", *policy_args).returncode == 1

    _edit_marked_line(project, '"store credit"', '"refund"')
    assert _run(project, "pytest", "--attest-report=r.json").returncode == 0
    assert _run(project, "attest", "policy", "evaluate", *policy_args).returncode == 0


@pytest.mark.integration
def test_generated_test_passes_beside_an_existing_conftest(project: Path) -> None:
    (project / "tests").mkdir()
    (project / "tests" / "conftest.py").write_text(
        "import pytest\n\n\n@pytest.fixture\ndef db():\n    return 1\n"
    )

    assert _run(project, "attest", "init").returncode == 0
    run = _run(project, "pytest")

    assert run.returncode == 0, run.stdout + run.stderr
    assert "1 passed" in run.stdout


# --- README snippets ---------------------------------------------------------


@dataclass
class Block:
    lang: str
    code: str
    line: int


class SnippetError(AssertionError):
    """A README code block failed or cannot be run."""


_FENCE = re.compile(r"^```([\w-]*)\n(.*?)^```$", re.MULTILINE | re.DOTALL)
# Display-only blocks (output, diagrams) and blocks the TypeScript README test runs.
_NOT_RUN_HERE = frozenset({"text", "ts"})
# Shell lines the test environment provides instead of running: project creation and
# package installation (the SDK under test is already installed) and the TypeScript
# quickstart, which the TypeScript README test runs.
_PROVIDED = re.compile(r"^(uv init|uv add|cd|pnpm)\b")


def readme_blocks(text: str) -> list[Block]:
    return [
        Block(m.group(1), m.group(2), text.count("\n", 0, m.start()) + 1)
        for m in _FENCE.finditer(text)
    ]


def run_readme_snippets(readme: Path, project: Path) -> None:
    """Run every python block as a test and every ``uv run`` shell command in ``project``.

    Raises SnippetError naming the block on the first failure.
    """
    assert _run(project, "attest", "init").returncode == 0
    for n, block in enumerate(readme_blocks(readme.read_text())):
        where = f"{readme.name}:{block.line}"
        if block.lang == "python":
            test_file = project / "tests" / f"test_readme_block_{n}.py"
            test_file.write_text(block.code)
            proc = _run(project, "pytest", str(test_file.relative_to(project)))
            if proc.returncode != 0 or "1 passed" not in proc.stdout:
                raise SnippetError(f"{where}: python block did not pass\n{proc.stdout}")
        elif block.lang in ("bash", "sh"):
            for command in _shell_commands(block.code):
                argv = shlex.split(command)
                if _PROVIDED.match(command):
                    continue
                if argv[:2] != ["uv", "run"] or argv[2:3] not in (["pytest"], ["attest"]):
                    raise SnippetError(f"{where}: no runner for shell command {command!r}")
                proc = _run(project, *argv[2:])
                if proc.returncode != 0:
                    raise SnippetError(
                        f"{where}: {command!r} exited {proc.returncode}\n{proc.stdout}{proc.stderr}"
                    )
        elif block.lang not in _NOT_RUN_HERE:
            raise SnippetError(f"{where}: code block language {block.lang!r} is not run")


def _shell_commands(code: str) -> list[str]:
    lines = [line.strip() for line in code.splitlines()]
    return [
        part.strip()
        for line in lines
        if line and not line.startswith("#")
        for part in line.split("&&")
    ]


@pytest.mark.integration
def test_readme_code_blocks_run(project: Path) -> None:
    run_readme_snippets(_README, project)


@pytest.mark.integration
def test_readme_snippet_test_fails_on_broken_snippet(project: Path) -> None:
    with pytest.raises(SnippetError, match="output_containz"):
        run_readme_snippets(_BROKEN_README, project)
