"""report-v2 writer for ``pytest --attest-report=<path>``.

The format is ``proto/attest/v1/report-v2.schema.json`` (contract C3): one row per
assertion in ``results``, each carrying the test id, file and line of the test that
requested it, and one row per test in ``tests`` with its outcome. ``attest policy
evaluate`` gates on ``results``; the pr-annotate action places inline comments with
``file`` and ``line``.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
)
from attest.result import AgentResult

REPORT_VERSION = 2

TEST_OUTCOMES = frozenset({"passed", "failed", "skipped"})


@dataclass
class TestRecord:
    """One test: identity, source location, runner outcome and its Attest results."""

    __test__ = False  # not a pytest test class

    test_id: str
    name: str
    file: str
    line: int
    outcome: str = "passed"
    duration_ms: int = 0
    agent_results: list[AgentResult] = field(default_factory=list)

    def __post_init__(self) -> None:
        if self.line < 1:
            raise ValueError(f"{self.test_id}: line must be >= 1, got {self.line}")
        if self.outcome not in TEST_OUTCOMES:
            raise ValueError(f"{self.test_id}: unknown outcome {self.outcome!r}")


def build_report(tests: list[TestRecord], *, simulated: bool) -> dict[str, Any]:
    """Return the report-v2 document for ``tests``."""
    rows: list[dict[str, Any]] = []
    test_rows: list[dict[str, Any]] = []
    total_cost = 0.0
    total_duration_ms = 0
    for test in tests:
        assertion_ids: list[str] = []
        reasons: list[str] = []
        for agent_result in test.agent_results:
            total_cost += agent_result.total_cost
            total_duration_ms += agent_result.total_duration_ms
            for result in agent_result.assertion_results:
                rows.append(
                    {
                        **result.to_dict(),
                        "test_id": test.test_id,
                        "file": test.file,
                        "line": test.line,
                    }
                )
                assertion_ids.append(result.assertion_id)
                if result.status == STATUS_NOT_EVALUATED and result.reason not in reasons:
                    reasons.append(str(result.reason))
        test_row: dict[str, Any] = {
            "test_id": test.test_id,
            "name": test.name,
            "file": test.file,
            "line": test.line,
            "outcome": test.outcome,
            "assertion_ids": assertion_ids,
            "duration_ms": test.duration_ms,
        }
        if reasons:
            test_row["not_evaluated_reasons"] = reasons
        test_rows.append(test_row)

    statuses = [row["status"] for row in rows]
    return {
        "report_version": REPORT_VERSION,
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "simulated": simulated,
        "summary": {
            "total": len(rows),
            "passed": statuses.count(STATUS_PASS),
            "soft_fail": statuses.count(STATUS_SOFT_FAIL),
            "hard_fail": statuses.count(STATUS_HARD_FAIL),
            "not_evaluated": statuses.count(STATUS_NOT_EVALUATED),
        },
        "results": rows,
        "tests": test_rows,
        "total_cost": total_cost,
        "total_duration_ms": total_duration_ms,
    }


def write_report(path: Path, tests: list[TestRecord], *, simulated: bool) -> None:
    """Write the report-v2 document for ``tests`` to ``path``, creating parent directories."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(build_report(tests, simulated=simulated), indent=2) + "\n")
