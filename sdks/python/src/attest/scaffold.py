"""Scaffold and validation utilities for Attest test suites."""

from __future__ import annotations

import sys
from datetime import datetime, timezone
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

PYPROJECT_SNIPPET = '''\
[project.optional-dependencies]
test = [
    "attest-ai",
    "pytest",
]
'''

_STALE_DAYS = 30


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


def validate_suite(target_dir: Path) -> None:
    """Validate assertion suite against engine capabilities.

    Checks for conftest.py and test files.
    Warns on golden trace files older than 30 days.
    """
    tests_dir = target_dir / "tests"
    conftest_path = tests_dir / "conftest.py"

    if not conftest_path.exists():
        print(
            f"Warning: no conftest.py found at {conftest_path}. "
            "Run `attest init` to scaffold.",
            file=sys.stderr,
        )

    test_files = list(tests_dir.glob("test_*.py")) if tests_dir.exists() else []
    if not test_files:
        print(
            f"Warning: no test files (test_*.py) found in {tests_dir}.",
            file=sys.stderr,
        )
    else:
        print(f"Found {len(test_files)} test file(s) in {tests_dir}")

    now = datetime.now(tz=timezone.utc)
    golden_files = list(target_dir.rglob("*.golden")) if target_dir.exists() else []
    for golden in golden_files:
        mtime = datetime.fromtimestamp(golden.stat().st_mtime, tz=timezone.utc)
        age_days = (now - mtime).days
        if age_days > _STALE_DAYS:
            print(
                f"Warning: golden trace {golden} is {age_days} days old "
                f"(>{_STALE_DAYS} days). Consider regenerating.",
                file=sys.stderr,
            )
