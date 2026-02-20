"""Agent execution result model."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    AssertionResult,
    Trace,
)

if TYPE_CHECKING:
    from attest.trace_tree import TraceTree


@dataclass
class AgentResult:
    """Result of an agent execution with assertion results."""

    trace: Trace
    assertion_results: list[AssertionResult] = field(default_factory=list)
    total_cost: float = 0.0
    total_duration_ms: int = 0

    @property
    def passed(self) -> bool:
        """True if all assertions passed."""
        return all(r.status == STATUS_PASS for r in self.assertion_results)

    @property
    def failed_assertions(self) -> list[AssertionResult]:
        """Return list of failed assertions (hard_fail or soft_fail)."""
        return [r for r in self.assertion_results if r.status != STATUS_PASS]

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
        """Number of failing assertions."""
        return len(self.assertion_results) - self.pass_count

    def trace_tree(self) -> TraceTree:
        """Build a TraceTree from this result's trace."""
        from attest.trace_tree import TraceTree
        return TraceTree(root=self.trace)
