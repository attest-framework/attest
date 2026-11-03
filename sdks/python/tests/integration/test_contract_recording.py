"""The pytest plugin sends the running test as ``evaluate_batch`` context, and an engine
started with ``ATTEST_CONTRACTS_DIR`` records it next to each assertion spec."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

from tests.integration.conftest import _engine_binary_path

_TEST_FILE = """\
from attest import expect
from attest._proto.types import Trace
from attest.result import AgentResult


def test_reply(attest):
    result = AgentResult(trace=Trace(trace_id="t1", output={"message": "refund done"}))
    attest.evaluate(expect(result).output_contains("refund"))
"""


@pytest.mark.integration
def test_recorded_specs_carry_test_id_file_and_line(tmp_path: Path) -> None:
    engine = _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    project = tmp_path / "project"
    (project / "tests").mkdir(parents=True)
    (project / "tests" / "test_x.py").write_text(_TEST_FILE)
    record_dir = tmp_path / "contracts"
    env = {
        **{k: v for k, v in os.environ.items() if not k.endswith("_API_KEY")},
        "HOME": str(tmp_path / "home"),
        "ATTEST_ENGINE_PATH": engine,
        "ATTEST_CACHE_DIR": str(tmp_path / "cache"),
        "ATTEST_CONTRACTS_DIR": str(record_dir),
    }

    run = subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider"],
        cwd=project,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )

    assert run.returncode == 0, run.stdout + run.stderr
    (record_file,) = record_dir.glob("contracts-*.ndjson")
    (entry,) = [json.loads(line) for line in record_file.read_text().splitlines()]
    assert entry["test_id"] == "tests/test_x.py::test_reply"
    assert entry["file"] == "tests/test_x.py"
    assert entry["line"] == 6
    assert entry["assertion"]["type"] == "content"
    assert "refund done" not in record_file.read_text()
