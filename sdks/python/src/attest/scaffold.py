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

# Every action the generated workflow uses, pinned to a commit SHA with its release tag in a
# trailing comment (a tag can be moved; a SHA cannot). The TypeScript scaffold carries the same
# table and a test keeps the two equal. The SHAs are the ones attest's own workflows use, and a
# test keeps those equal too: Dependabot bumps the workflows, that test fails until this table
# follows, so a new release ships current pins. The pins are then fixed in a generated project;
# its own Dependabot config (not written by `attest init`) would bump them from there.
ACTION_PINS: dict[str, tuple[str, str]] = {
    "actions/checkout": ("3d3c42e5aac5ba805825da76410c181273ba90b1", "v7.0.1"),
    "astral-sh/setup-uv": ("68559e99cc807c1eb7e9238fd43a15bba460eec2", "v10.3.3"),
    "actions/upload-artifact": ("cf430e030ddbb5b0abf93d22962f4752f3646cd9", "v7.0.2"),
    "actions/download-artifact": ("9000827ccba6bdab643e8b6fd33ac0654aef8333", "v8.0.2"),
    "attest-framework/attest/.github/actions/pr-annotate": (
        "5d5b748b13da7ba034b3acfce631cee8fcae720c",
        "v1.0.1",
    ),
}

ANNOTATE_ACTION = "attest-framework/attest/.github/actions/pr-annotate"
# pr-annotate reads the engine from ~/.attest/bin/<version>/ and never downloads it, so the
# annotate job fetches exactly the release the pr-annotate pin's tag names.
ANNOTATE_ENGINE_VERSION = ACTION_PINS[ANNOTATE_ACTION][1].removeprefix("v")
# The annotate job holds a write token, so the install resolves attest-ai's dependencies as of one
# day after the ANNOTATE_ENGINE_VERSION release (attest-ai 1.0.1 was uploaded 2026-11-05) instead of
# whatever is newest on the runner. Move it with the version.
ANNOTATE_EXCLUDE_NEWER = "2026-11-06"
_ANNOTATE_FETCH_COMMAND = (
    f"uvx --no-config --exclude-newer {ANNOTATE_EXCLUDE_NEWER} "
    f'--from "attest-ai=={ANNOTATE_ENGINE_VERSION}" attest doctor'
)


def _uses(action: str) -> str:
    sha, version = ACTION_PINS[action]
    return f"{action}@{sha} # {version}"


GITHUB_WORKFLOW_TEMPLATE = f"""\
# Written by `attest init --ci github`.
# The test step records its outcome and continues so the report is always uploaded,
# the JUnit file written, the policy evaluated and the pull request summarised; the last
# step fails the job when the tests failed. A policy warning (exit 2) does not fail the job.
# Only the annotate job can write to the pull request; the job that runs your tests cannot.
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
    steps:
      - uses: {_uses("actions/checkout")}
        with:
          persist-credentials: false
      - uses: {_uses("astral-sh/setup-uv")}
      - name: Install dependencies
        run: uv sync --locked
      - name: Run tests
        id: tests
        continue-on-error: true
        # Traces (prompts, outputs, tool args; not redacted) travel in the artifact; on a public
        # repository any signed-in GitHub user can download it. Enable on private repositories only:
        # env:
        #   ATTEST_REPORT_TRACES: "1"
        run: uv run pytest --attest-report=attest-report.json
      - name: Write the JUnit report
        if: always()
        run: uv run attest report junit --report attest-report.json --out attest-junit.xml
      - name: Write the failure viewer
        if: always()
        run: uv run attest view attest-report.json --no-open --out attest-report.html
      - name: Upload the Attest report
        if: always()
        uses: {_uses("actions/upload-artifact")}
        with:
          name: attest-report
          path: |
            attest-report.json
            attest-junit.xml
            attest-report.html
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
      - name: Fail the job when tests failed
        if: always() && steps.tests.outcome == 'failure'
        run: |
          echo "::error::pytest failed; see the Run tests step"
          exit 1

  annotate:
    # Posts one summary comment (updated on every run) and annotates failures at their
    # file and line. Fork pull requests get a read-only token that cannot comment.
    needs: attest
    if: >-
      always() && github.event_name == 'pull_request' &&
      github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: {_uses("actions/download-artifact")}
        with:
          name: attest-report
      - uses: {_uses("astral-sh/setup-uv")}
        with:
          # This job holds the write token and must not restore a cache the test job filled.
          enable-cache: false
      - name: Fetch the attest engine
        # pr-annotate only finds an engine, it never downloads one. The first `attest` command of
        # the pinned release fetches it into ~/.attest/bin/<version>/. Run from the runner's temp
        # directory without project config, so nothing the pull request controls steers the install.
        working-directory: ${{{{ runner.temp }}}}
        run: {_ANNOTATE_FETCH_COMMAND}
      - uses: {_uses(ANNOTATE_ACTION)}
        with:
          report: attest-report.json
          engine-version: {ANNOTATE_ENGINE_VERSION}
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
