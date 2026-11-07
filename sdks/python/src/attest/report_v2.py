"""report-v2 writer for ``pytest --attest-report=<path>``.

The format is ``proto/attest/v1/report-v2.schema.json`` (contract C3): one row per
assertion in ``results``, each carrying the test id, file and line of the test that
requested it, and one row per test in ``tests`` with its outcome. ``attest policy
evaluate`` gates on ``results``; the pr-annotate action places inline comments with
``file`` and ``line``.
"""

from __future__ import annotations

import json
import os
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
    Trace,
    Usage,
)
from attest.assertion_id import ID_SCHEME
from attest.replay import MODE_AUTO, ReplaySummary
from attest.result import AgentResult

REPORT_VERSION = 2

TEST_OUTCOMES = frozenset({"passed", "failed", "skipped"})

#: Set to ``1`` to embed the evaluated traces in the report (``traces`` and each row's
#: ``trace_id``) for ``attest view``. Off by default: traces hold prompts, model outputs
#: and tool arguments.
REPORT_TRACES_ENV = "ATTEST_REPORT_TRACES"


def report_traces_enabled() -> bool:
    """True when ``ATTEST_REPORT_TRACES=1``."""
    return os.environ.get(REPORT_TRACES_ENV, "").strip() == "1"


class _TraceTable:
    """The report's ``traces`` map: one entry per distinct trace.

    Equal traces share an id. Distinct traces that share a ``trace_id`` (trial loops reuse
    one) get ``<id>#2``, ``<id>#3``..., and a trace without an id gets ``trace-<n>``, so a
    row's ``trace_id`` always names exactly the trace it was evaluated against.
    """

    def __init__(self) -> None:
        self.traces: dict[str, dict[str, Any]] = {}

    def add(self, trace: Trace) -> str:
        doc = trace.to_dict()
        base = trace.trace_id or f"trace-{len(self.traces) + 1}"
        key, n = base, 1
        while key in self.traces:
            if self.traces[key] == doc:
                return key
            n += 1
            key = f"{base}#{n}"
        self.traces[key] = doc
        return key


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


def build_report(
    tests: list[TestRecord],
    *,
    simulated: bool,
    usage: Usage,
    include_traces: bool = False,
    tests_collected: int | None = None,
) -> dict[str, Any]:
    """Return the report-v2 document for ``tests``; ``usage`` is the session's
    attest model usage (``summary.usage``). ``include_traces`` adds the ``traces`` map and
    a ``trace_id`` on every row evaluated against a trace. ``tests_collected`` is the
    session's collected-test count; a xdist worker leaves it ``None`` (the controller sets it)."""
    table = _TraceTable() if include_traces else None
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
            trace_id = table.add(agent_result.trace) if table is not None else None
            for result in agent_result.assertion_results:
                row = {
                    **result.to_dict(),
                    "test_id": test.test_id,
                    "file": test.file,
                    "line": test.line,
                }
                if trace_id is not None:
                    row["trace_id"] = trace_id
                rows.append(row)
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
    document: dict[str, Any] = {
        "report_version": REPORT_VERSION,
        "id_scheme": ID_SCHEME,
        "effect_world_excluded": True,
        "budget_exceeded": any(r.budget_exceeded for test in tests for r in test.agent_results),
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "simulated": simulated,
        "summary": summary_block,
        "results": rows,
        "tests": test_rows,
        "total_cost": total_cost,
        "total_duration_ms": total_duration_ms,
    }
    if tests_collected is not None:
        document["tests_collected"] = tests_collected
    if table is not None:
        document["traces"] = table.traces
    return document


def merge_reports(
    documents: list[dict[str, Any]], *, tests_collected: int, usage: Usage
) -> dict[str, Any]:
    """Merge the per-worker report documents of a pytest-xdist run into one.

    Rows and tests are concatenated in the order given; the summary counts are recomputed
    from the merged rows, ``usage`` is the controller's sum of every worker's usage, and
    the replay block, total cost and duration are summed. Trace ids that collide across
    workers with different traces are renumbered, and the rows that reference them follow.
    """
    if not documents:
        raise ValueError("merge_reports needs at least one worker report")
    simulated = {bool(doc["simulated"]) for doc in documents}
    if len(simulated) != 1:
        raise ValueError("worker reports disagree on simulated")
    results: list[dict[str, Any]] = []
    test_rows: list[dict[str, Any]] = []
    traces: dict[str, dict[str, Any]] = {}
    replay_blocks: list[dict[str, Any]] = []
    for doc in documents:
        remap = _merge_traces(traces, doc.get("traces", {}))
        for row in doc["results"]:
            if "trace_id" in row:
                row = {**row, "trace_id": remap[row["trace_id"]]}
            results.append(row)
        test_rows.extend(doc["tests"])
        if "replay" in doc["summary"]:
            replay_blocks.append(doc["summary"]["replay"])
    statuses = [row["status"] for row in results]
    summary_block: dict[str, Any] = {
        "total": len(results),
        "passed": statuses.count(STATUS_PASS),
        "soft_fail": statuses.count(STATUS_SOFT_FAIL),
        "hard_fail": statuses.count(STATUS_HARD_FAIL),
        "not_evaluated": statuses.count(STATUS_NOT_EVALUATED),
        "usage": usage.to_dict(),
    }
    if replay_blocks:
        summary_block["replay"] = {
            key: sum(block[key] for block in replay_blocks) for key in replay_blocks[0]
        }
    merged: dict[str, Any] = {
        "report_version": REPORT_VERSION,
        "id_scheme": ID_SCHEME,
        "effect_world_excluded": all(
            doc.get("effect_world_excluded") is True
            or not any(row.get("type") == "effect" for row in doc["results"])
            for doc in documents
        ),
        "budget_exceeded": any(doc["budget_exceeded"] for doc in documents),
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "simulated": simulated.pop(),
        "summary": summary_block,
        "results": results,
        "tests": test_rows,
        "total_cost": sum(doc["total_cost"] for doc in documents),
        "total_duration_ms": sum(doc["total_duration_ms"] for doc in documents),
        "tests_collected": tests_collected,
    }
    if traces:
        merged["traces"] = traces
    return merged


def _merge_traces(
    into: dict[str, dict[str, Any]], incoming: dict[str, dict[str, Any]]
) -> dict[str, str]:
    """Add ``incoming`` traces to ``into``; return each incoming key's key in ``into``."""
    remap: dict[str, str] = {}
    for key, doc in incoming.items():
        base, n, merged_key = key.split("#", 1)[0], 1, key
        while merged_key in into and into[merged_key] != doc:
            n += 1
            merged_key = f"{base}#{n}"
        into[merged_key] = doc
        remap[key] = merged_key
    return remap


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


def write_report(
    path: Path,
    tests: list[TestRecord],
    *,
    simulated: bool,
    usage: Usage,
    tests_collected: int,
) -> None:
    """Write the report-v2 document for ``tests`` to ``path``, creating parent directories.

    Embeds the traces when ``ATTEST_REPORT_TRACES=1``.
    """
    write_document(
        path,
        build_report(
            tests,
            simulated=simulated,
            usage=usage,
            include_traces=report_traces_enabled(),
            tests_collected=tests_collected,
        ),
    )


def write_document(path: Path, report: dict[str, Any]) -> None:
    """Write an already built report document to ``path``, creating parent directories."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2) + "\n")
