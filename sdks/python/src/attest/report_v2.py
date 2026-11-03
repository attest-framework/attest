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
    REASON_REPLAY_MISS,
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    AssertionResult,
    Usage,
)
from attest.replay import MODE_AUTO, ReplaySummary
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
    #: Fixed-N trials actually run (``--attest-trials`` > 1); ``None`` for a single run.
    trials: int | None = None
    #: What the test's replay sessions served; empty when it did not replay a fixture.
    replay: list[ReplaySummary] = field(default_factory=list)

    def __post_init__(self) -> None:
        if self.line < 1:
            raise ValueError(f"{self.test_id}: line must be >= 1, got {self.line}")
        if self.outcome not in TEST_OUTCOMES:
            raise ValueError(f"{self.test_id}: unknown outcome {self.outcome!r}")
        if self.trials is not None and self.trials < 1:
            raise ValueError(f"{self.test_id}: trials must be >= 1, got {self.trials}")


def build_report(tests: list[TestRecord], *, simulated: bool, usage: Usage) -> dict[str, Any]:
    """Return the report-v2 document for ``tests``; ``usage`` is the session's
    attest model usage (``summary.usage``)."""
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
                if result.assertion_id not in assertion_ids:
                    assertion_ids.append(result.assertion_id)
                if result.status == STATUS_NOT_EVALUATED and result.reason not in reasons:
                    reasons.append(str(result.reason))
        for summary in test.replay:
            if summary.mode == MODE_AUTO and summary.misses:
                row = _replay_miss_result(summary).to_dict()
                rows.append({**row, "test_id": test.test_id, "file": test.file, "line": test.line})
                assertion_ids.append(row["assertion_id"])
                if REASON_REPLAY_MISS not in reasons:
                    reasons.append(REASON_REPLAY_MISS)
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
        if test.trials is not None:
            test_row["trials"] = test.trials
        if test.replay:
            test_row["replay"] = _test_replay(test.replay)
        test_rows.append(test_row)

    statuses = [row["status"] for row in rows]
    summary_block: dict[str, Any] = {
        "total": len(rows),
        "passed": statuses.count(STATUS_PASS),
        "soft_fail": statuses.count(STATUS_SOFT_FAIL),
        "hard_fail": statuses.count(STATUS_HARD_FAIL),
        "not_evaluated": statuses.count(STATUS_NOT_EVALUATED),
        "usage": usage.to_dict(),
    }
    replays = [s for test in tests for s in test.replay]
    if replays:
        summary_block["replay"] = {
            "replayed": sum(s.replayed for s in replays),
            "live": sum(s.live for s in replays),
            "misses": sum(s.misses for s in replays),
            "live_cost_usd": sum(s.live_cost_usd for s in replays),
            "unpriced_live_calls": sum(s.unpriced_live_calls for s in replays),
        }
    return {
        "report_version": REPORT_VERSION,
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "simulated": simulated,
        "summary": summary_block,
        "results": rows,
        "tests": test_rows,
        "total_cost": total_cost,
        "total_duration_ms": total_duration_ms,
    }


def _replay_miss_result(summary: ReplaySummary) -> AssertionResult:
    """The ``not_evaluated`` row of a test whose auto-mode replay missed with no provider key."""
    return AssertionResult(
        assertion_id=f"replay_{summary.fixture}",
        status=STATUS_NOT_EVALUATED,
        score=0.0,
        explanation=summary.first_miss or "replay miss",
        reason=REASON_REPLAY_MISS,
    )


def _test_replay(summaries: list[ReplaySummary]) -> dict[str, Any]:
    """The ``tests[].replay`` object: one session's, or the sum of a test's sessions."""
    if len(summaries) == 1:
        return summaries[0].to_report()
    fixtures = list(dict.fromkeys(s.fixture for s in summaries))
    modes = {s.mode for s in summaries}
    row: dict[str, Any] = {
        "fixtures": fixtures,
        "mode": summaries[0].mode if len(modes) == 1 else MODE_AUTO,
        "replayed": sum(s.replayed for s in summaries),
        "live": sum(s.live for s in summaries),
        "misses": sum(s.misses for s in summaries),
    }
    miss = next((s.first_miss for s in summaries if s.first_miss), None)
    if miss is not None:
        row["miss"] = miss
    return row


def write_report(path: Path, tests: list[TestRecord], *, simulated: bool, usage: Usage) -> None:
    """Write the report-v2 document for ``tests`` to ``path``, creating parent directories."""
    path.parent.mkdir(parents=True, exist_ok=True)
    report = build_report(tests, simulated=simulated, usage=usage)
    path.write_text(json.dumps(report, indent=2) + "\n")
