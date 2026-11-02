"""Agent execution result model."""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Protocol

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    AssertionResult,
    Trace,
    Usage,
)

#: Test outcomes derived from a batch of assertion results (``AgentResult.outcome``).
OUTCOME_PASS = "pass"
OUTCOME_FAIL = "fail"
OUTCOME_SKIP = "skip"


def aggregate_outcomes(outcomes: list[str]) -> str:
    """Combine per-run outcomes: any fail → fail; else any skip → skip; else pass."""
    if OUTCOME_FAIL in outcomes:
        return OUTCOME_FAIL
    if OUTCOME_SKIP in outcomes:
        return OUTCOME_SKIP
    return OUTCOME_PASS


class Verdict(Protocol):
    """Anything ``attest.enforce()`` can turn into a test outcome:
    ``AgentResult``, ``RepeatResult`` and ``ScenarioResult``. Aggregates
    nest: a ``RepeatResult`` may hold ``ScenarioResult`` trials."""

    @property
    def not_evaluated_reasons(self) -> dict[str, int]: ...

    def outcome(self, *, strict: bool = False) -> str: ...

    def skip_reason(self) -> str: ...

    def render_diagnostics(self, *, color: bool = False) -> str: ...


def format_skip_reason(counts: dict[str, int]) -> str:
    """``"not evaluated: <reason>[, <reason> ×n ...]"`` from per-reason counts."""
    parts = [reason if n == 1 else f"{reason} ×{n}" for reason, n in counts.items()]
    return "not evaluated: " + ", ".join(parts)


def trials_not_evaluated_reasons(results: Sequence[Verdict]) -> dict[str, int]:
    """Not-evaluated reasons summed across trials, in first-seen order."""
    counts: dict[str, int] = {}
    for r in results:
        for reason, n in r.not_evaluated_reasons.items():
            counts[reason] = counts.get(reason, 0) + n
    return counts


def trials_skip_reason(results: Sequence[Verdict]) -> str:
    """Skip reason for a set of trials: not-evaluated reasons summed across them."""
    return format_skip_reason(trials_not_evaluated_reasons(results))


def trials_diagnostics(results: Sequence[Verdict], *, label: str, color: bool = False) -> str:
    """Diagnostics of every trial whose outcome is not pass, headed by its index."""
    blocks = [
        f"{label} {i}/{len(results)}:{r.render_diagnostics(color=color)}"
        for i, r in enumerate(results, start=1)
        if r.outcome() != OUTCOME_PASS
    ]
    return "\n".join(blocks) if blocks else f"  (no failing {label}s)\n"


if TYPE_CHECKING:
    from attest.trace_tree import TraceTree


@dataclass
class AgentResult:
    """Result of an agent execution with assertion results."""

    trace: Trace
    assertion_results: list[AssertionResult] = field(default_factory=list)
    total_cost: float = 0.0
    total_duration_ms: int = 0
    #: Attest-initiated model usage of the ``evaluate_batch`` call.
    usage: Usage = field(default_factory=Usage)

    @property
    def passed(self) -> bool:
        """True only if every assertion has status ``pass`` (strict).

        Not the test verdict: soft failures pass a test and not-evaluated
        assertions skip it. Use ``outcome()`` for the verdict.
        """
        return all(r.status == STATUS_PASS for r in self.assertion_results)

    def outcome(self, *, strict: bool = False) -> str:
        """Test outcome after the whole batch: ``"fail"``, ``"skip"`` or ``"pass"``.

        Any ``hard_fail`` fails. Otherwise any ``not_evaluated`` skips
        (fails when ``strict``). Otherwise the test passes, including when
        only ``soft_fail`` rows are present (see ``soft_failures``).
        """
        if self.hard_failures:
            return OUTCOME_FAIL
        if self.not_evaluated:
            return OUTCOME_FAIL if strict else OUTCOME_SKIP
        return OUTCOME_PASS

    @property
    def failed_assertions(self) -> list[AssertionResult]:
        """Return failed assertions (``hard_fail`` and ``soft_fail`` only)."""
        return [
            r for r in self.assertion_results if r.status in (STATUS_HARD_FAIL, STATUS_SOFT_FAIL)
        ]

    @property
    def not_evaluated(self) -> list[AssertionResult]:
        """Return assertions the engine did not evaluate (each carries a ``reason``)."""
        return [r for r in self.assertion_results if r.status == STATUS_NOT_EVALUATED]

    @property
    def not_evaluated_reasons(self) -> dict[str, int]:
        """Count of ``not_evaluated`` assertions per reason, in first-seen order."""
        counts: dict[str, int] = {}
        for r in self.not_evaluated:
            key = r.reason or "unknown"
            counts[key] = counts.get(key, 0) + 1
        return counts

    @property
    def pass_rate(self) -> float | None:
        """Passed / evaluated assertions; ``None`` when nothing was evaluated.

        ``not_evaluated`` rows are excluded from the denominator; their count
        is reported by ``not_evaluated``.
        """
        evaluated = len(self.assertion_results) - len(self.not_evaluated)
        if evaluated == 0:
            return None
        return self.pass_count / evaluated

    @property
    def hard_failures(self) -> list[AssertionResult]:
        """Return list of hard failures only."""
        return [r for r in self.assertion_results if r.status == STATUS_HARD_FAIL]

    @property
    def soft_failures(self) -> list[AssertionResult]:
        """Return list of soft failures only."""
        return [r for r in self.assertion_results if r.status == STATUS_SOFT_FAIL]

    @property
    def pass_count(self) -> int:
        """Number of passing assertions."""
        return sum(1 for r in self.assertion_results if r.status == STATUS_PASS)

    @property
    def fail_count(self) -> int:
        """Number of failing assertions (``hard_fail`` + ``soft_fail``)."""
        return len(self.failed_assertions)

    def skip_reason(self) -> str:
        """``"not evaluated: <reason>[, <reason> ...]"`` for skipped tests."""
        return format_skip_reason(self.not_evaluated_reasons)

    def trace_tree(self) -> TraceTree:
        """Build a TraceTree from this result's trace."""
        from attest.trace_tree import TraceTree

        return TraceTree(root=self.trace)

    def render_diagnostics(self, *, color: bool = False) -> str:
        """Pytest-style diagnostic block listing every failing assertion.

        Use as the message argument to a pytest ``assert`` so reviewers
        see trace path, expected vs actual, and judge metadata for each
        failure rather than just ``assert False``.

        Example::

            assert agent_result.outcome() != "fail", agent_result.render_diagnostics()
        """
        from attest.cli.diagnostics import render_diagnostics

        return render_diagnostics(self, color=color)
