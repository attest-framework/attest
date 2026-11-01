from __future__ import annotations

import functools
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any

from attest.result import (
    OUTCOME_PASS,
    OUTCOME_SKIP,
    Verdict,
    aggregate_outcomes,
    trials_diagnostics,
    trials_not_evaluated_reasons,
    trials_skip_reason,
)


@dataclass
class RepeatResult:
    """Results of ``n`` trials, aggregated under the assertion-status rules.

    Each trial should come from ``attest.inspect()``, which never enforces,
    so one failing trial does not abort the loop. Enforce the aggregate with
    ``attest.enforce(repeat_result)``.
    """

    results: list[Verdict] = field(default_factory=list)

    @property
    def count(self) -> int:
        return len(self.results)

    @property
    def not_evaluated(self) -> int:
        """Trials skipped because an assertion was not evaluated."""
        return sum(1 for r in self.results if r.outcome() == OUTCOME_SKIP)

    @property
    def pass_rate(self) -> float | None:
        """Passing / evaluated trials; ``None`` when no trial was evaluated.

        Trials whose outcome is skip (``not_evaluated``) are excluded from
        the denominator and counted in ``not_evaluated``.
        """
        evaluated = self.count - self.not_evaluated
        if evaluated == 0:
            return None
        passed = sum(1 for r in self.results if r.outcome() == OUTCOME_PASS)
        return passed / evaluated

    @property
    def all_passed(self) -> bool:
        """True when every trial's outcome is pass (soft failures included)."""
        return all(r.outcome() == OUTCOME_PASS for r in self.results)

    def outcome(self, *, strict: bool = False) -> str:
        """Aggregate verdict: any failing trial fails; otherwise any skipped
        trial skips (fails when ``strict``); otherwise pass."""
        return aggregate_outcomes([r.outcome(strict=strict) for r in self.results])

    @property
    def not_evaluated_reasons(self) -> dict[str, int]:
        """Not-evaluated reasons summed across trials."""
        return trials_not_evaluated_reasons(self.results)

    def skip_reason(self) -> str:
        """Not-evaluated reasons summed across trials."""
        return trials_skip_reason(self.results)

    def render_diagnostics(self, *, color: bool = False) -> str:
        """Diagnostics of every trial that did not pass."""
        return trials_diagnostics(self.results, label="trial", color=color)


def repeat(n: int) -> Callable[[Callable[..., Verdict]], Callable[..., RepeatResult]]:
    """Run the wrapped function ``n`` times. It may return any ``Verdict``:
    an ``AgentResult`` from ``attest.inspect()`` or a ``ScenarioResult``."""

    def decorator(fn: Callable[..., Verdict]) -> Callable[..., RepeatResult]:
        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> RepeatResult:
            results: list[Verdict] = []
            for _ in range(n):
                result = fn(*args, **kwargs)
                results.append(result)
            return RepeatResult(results=results)

        return wrapper

    return decorator
