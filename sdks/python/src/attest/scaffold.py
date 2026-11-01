"""Scaffolding for ``attest init``."""

from __future__ import annotations

import sys
from pathlib import Path

CONFTEST_TEMPLATE = '''\
"""Attest test fixtures."""
import attest


@attest.agent("my-agent")
def my_agent(builder, **kwargs):
    # Replace with your agent implementation
    return {"result": "hello"}
'''

SAMPLE_TEST_TEMPLATE = '''\
"""Sample Attest test."""
from attest import expect


def test_my_agent(my_agent_result):
    expect(my_agent_result).output_contains("hello")
'''

PYPROJECT_SNIPPET = """\
[project.optional-dependencies]
test = [
    "attest-ai",
    "pytest",
]
"""


def scaffold_project(target_dir: Path) -> None:
    """Create initial attest project structure.

    Creates tests/ directory, conftest.py, and a sample test file.
    Does not overwrite existing files.
    """
    tests_dir = target_dir / "tests"
    conftest_path = tests_dir / "conftest.py"
    sample_test_path = tests_dir / "test_my_agent.py"

    tests_dir.mkdir(parents=True, exist_ok=True)
    print(f"Directory: {tests_dir}")

    if conftest_path.exists():
        print(f"Skipped (exists): {conftest_path}", file=sys.stderr)
    else:
        conftest_path.write_text(CONFTEST_TEMPLATE)
        print(f"Created: {conftest_path}")

    if sample_test_path.exists():
        print(f"Skipped (exists): {sample_test_path}", file=sys.stderr)
    else:
        sample_test_path.write_text(SAMPLE_TEST_TEMPLATE)
        print(f"Created: {sample_test_path}")
