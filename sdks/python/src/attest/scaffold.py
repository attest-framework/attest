"""Scaffolding for ``attest init``.

``attest init`` writes a sample test that runs with no model, API key or network: the
agent run is a trace built by hand with ``TraceBuilder`` and every assertion is
deterministic (layers 1-4). ``attest init --ci github`` also writes a GitHub Actions
workflow that gates merges on the test run, the report and the policy.
"""

from __future__ import annotations

import sys
from dataclasses import dataclass, field
from pathlib import Path

from attest import __version__

# The line a new user edits to see a failure. README.md and the generated-project
# tests point at it by this marker.
DEMO_MARKER = "ATTEST-DEMO"

SAMPLE_TEST_TEMPLATE = f'''\
"""Sample Attest test written by `attest init`. Run it with `pytest`.

`my_agent_result` is the trace of one agent run, built by hand with TraceBuilder, so
the sample needs no model, API key or network. Replace it with a trace from your
agent: wrap the agent with @attest.agent or record it with an adapter such as
attest.OpenAIAdapter.
"""

from __future__ import annotations

import pytest

from attest import Trace, TraceBuilder, expect
from attest.plugin import AttestEngineFixture


@pytest.fixture
def my_agent_result() -> Trace:
    return (
        TraceBuilder(agent_id="refund-agent")
        .set_input(user_message="Please refund order ORD-1001.")
        .add_tool_call(
            "lookup_order",
            args={{"order_id": "ORD-1001"}},
            result={{"status": "delivered", "amount": 42.0}},
        )
        .add_tool_call(
            "issue_refund",
            args={{"order_id": "ORD-1001", "amount": 42.0}},
            result={{"refund_id": "RF-1001"}},
        )
        .set_output(
            message="Your refund of $42.00 has been issued.",
            structured={{"refund_id": "RF-1001", "amount": 42.0}},
        )
        .set_metadata(total_tokens=180, cost_usd=0.002, latency_ms=900)
        .build()
    )


def test_my_agent(attest: AttestEngineFixture, my_agent_result: Trace) -> None:
    chain = (
        expect(my_agent_result)
        .output_matches_schema({{"type": "object", "required": ["refund_id"]}})  # layer 1: schema
        .cost_under(0.01)  # layer 2: constraint
        .tools_called_in_order(["lookup_order", "issue_refund"])  # layer 3: trace
        .output_contains("refund")  # {DEMO_MARKER}: change "refund" to "store credit"
    )
    attest.evaluate(chain)
'''

POLICY_TEMPLATE = """\
# Merge policy for `attest policy evaluate --report <report> --policy attest.policy.yaml`.
# Exit 0: pass. Exit 1: a blocking rule failed. Exit 2: warnings only.
layers:
  0:  # layer 0 caps every layer together
    max_hard_fails: 0
# Assertions that could not run (no judge configured, simulation) warn instead of block.
not_evaluated: warn
"""

GITHUB_WORKFLOW_TEMPLATE = f"""\
# Written by `attest init --ci github`.
# The test step records its outcome and continues so the report is always uploaded,
# the JUnit file written, the policy evaluated and the pull request summarised; the last
# step fails the job when the tests failed. A policy warning (exit 2) does not fail the job.
name: attest

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  attest:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: actions/checkout@v6
        with:
          persist-credentials: false
      - uses: astral-sh/setup-uv@v7
      - name: Install dependencies
        run: uv sync --locked
      - name: Run tests
        id: tests
        continue-on-error: true
        run: uv run pytest --attest-report=attest-report.json
      - name: Write the JUnit report
        if: always()
        run: uv run attest report junit --report attest-report.json --out attest-junit.xml
      - name: Upload the Attest report
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: attest-report
          path: |
            attest-report.json
            attest-junit.xml
          if-no-files-found: error
      - name: Evaluate the merge policy
        if: always()
        run: |
          set +e
          uv run attest policy evaluate --report attest-report.json --policy attest.policy.yaml
          code=$?
          if [ "$code" -eq 2 ]; then
            echo "::warning::attest policy: warnings only (exit 2)"
            exit 0
          fi
          exit "$code"
      - name: Annotate the pull request
        # Posts one summary comment (updated on every run) and annotates failures at their
        # file and line. Fork pull requests get a read-only token that cannot comment; the
        # last step still fails the job when tests failed.
        if: >-
          always() && github.event_name == 'pull_request' &&
          github.event.pull_request.head.repo.full_name == github.repository
        uses: attest-framework/attest/.github/actions/pr-annotate@v{__version__}
        with:
          report: attest-report.json
      - name: Fail the job when tests failed
        if: always() && steps.tests.outcome == 'failure'
        run: |
          echo "::error::pytest failed; see the Run tests step"
          exit 1
"""

CI_PROVIDERS = ("github",)


@dataclass(frozen=True)
class ScaffoldResult:
    """Files ``scaffold_project`` wrote (``created``) and left alone because they existed
    (``skipped``), as forward-slash paths relative to the target directory."""

    created: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)


def _write(
    target_dir: Path, rel: str, content: str, result: ScaffoldResult, *, quiet: bool
) -> None:
    path = target_dir / rel
    if path.exists():
        result.skipped.append(rel)
        if not quiet:
            print(f"Skipped (exists): {path}", file=sys.stderr)
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)
    result.created.append(rel)
    if not quiet:
        print(f"Created: {path}")


def scaffold_project(
    target_dir: Path, *, ci: str | None = None, quiet: bool = False
) -> ScaffoldResult:
    """Write the sample test (with its ``my_agent_result`` fixture) and the merge policy.

    ``ci="github"`` also writes ``.github/workflows/attest.yml``. Files go under
    ``target_dir``; existing files are never overwritten, and the sample test is
    self-contained so an existing ``tests/conftest.py`` does not affect it. ``quiet``
    suppresses the per-file lines and the next-step hint. Returns what was written.
    """
    if ci is not None and ci not in CI_PROVIDERS:
        raise ValueError(f"unknown CI provider {ci!r}; supported: {', '.join(CI_PROVIDERS)}")
    result = ScaffoldResult()
    _write(target_dir, "tests/test_my_agent.py", SAMPLE_TEST_TEMPLATE, result, quiet=quiet)
    _write(target_dir, "attest.policy.yaml", POLICY_TEMPLATE, result, quiet=quiet)
    if ci == "github":
        _write(
            target_dir,
            ".github/workflows/attest.yml",
            GITHUB_WORKFLOW_TEMPLATE,
            result,
            quiet=quiet,
        )
    if not quiet:
        print(
            "\nNext: run `pytest`. Then change the line marked "
            f"{DEMO_MARKER} in tests/test_my_agent.py and run it again to see a failure."
        )
    return result
