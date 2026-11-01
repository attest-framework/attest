from __future__ import annotations

import functools
import random
import time
from collections.abc import Callable
from contextvars import Token
from dataclasses import dataclass, field
from typing import Any

from attest.result import (
    OUTCOME_PASS,
    AgentResult,
    aggregate_outcomes,
    trials_diagnostics,
    trials_not_evaluated_reasons,
    trials_skip_reason,
)
from attest.simulation._context import _active_mock_registry
from attest.simulation.personas import Persona


@dataclass
class ScenarioConfig:
    persona: Persona | None = None
    mock_tools: dict[str, Callable[..., Any]] = field(default_factory=dict)
    fault_error_rate: float = 0.0
    fault_latency_jitter_ms: int = 0
    max_turns: int = 1
    seed: int | None = None


@dataclass
class ScenarioResult:
    config: ScenarioConfig
    results: list[AgentResult] = field(default_factory=list)

    @property
    def passed(self) -> bool:
        """True when the aggregate ``outcome()`` is pass."""
        return self.outcome() == OUTCOME_PASS

    def outcome(self, *, strict: bool = False) -> str:
        """Aggregate verdict over turns: any failing turn fails; otherwise any
        skipped turn skips (fails when ``strict``); otherwise pass."""
        return aggregate_outcomes([r.outcome(strict=strict) for r in self.results])

    @property
    def not_evaluated_reasons(self) -> dict[str, int]:
        """Not-evaluated reasons summed across turns."""
        return trials_not_evaluated_reasons(self.results)

    def skip_reason(self) -> str:
        """Not-evaluated reasons summed across turns."""
        return trials_skip_reason(self.results)

    def render_diagnostics(self, *, color: bool = False) -> str:
        """Diagnostics of every turn that did not pass."""
        return trials_diagnostics(self.results, label="turn", color=color)


def scenario(
    persona: Persona | None = None,
    mock_tools: dict[str, Callable[..., Any]] | None = None,
    fault_error_rate: float = 0.0,
    fault_latency_jitter_ms: int = 0,
    max_turns: int = 1,
    seed: int | None = None,
) -> Callable[[Callable[..., AgentResult]], Callable[..., ScenarioResult]]:
    config = ScenarioConfig(
        persona=persona,
        mock_tools=mock_tools or {},
        fault_error_rate=fault_error_rate,
        fault_latency_jitter_ms=fault_latency_jitter_ms,
        max_turns=max_turns,
        seed=seed,
    )

    def decorator(fn: Callable[..., AgentResult]) -> Callable[..., ScenarioResult]:
        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> ScenarioResult:
            token: Token[dict[str, Callable[..., Any]] | None] | None = None
            if config.mock_tools:
                token = _active_mock_registry.set(dict(config.mock_tools))

            try:
                results: list[AgentResult] = []
                rng = random.Random(config.seed)

                for turn in range(config.max_turns):
                    if config.fault_error_rate > 0 and rng.random() < config.fault_error_rate:
                        raise RuntimeError(f"Injected fault at turn {turn}")

                    if config.fault_latency_jitter_ms > 0:
                        jitter = rng.uniform(0, config.fault_latency_jitter_ms / 1000.0)
                        time.sleep(jitter)

                    call_kwargs = dict(kwargs)
                    if config.persona is not None and "persona" not in call_kwargs:
                        call_kwargs["persona"] = config.persona

                    result = fn(*args, **call_kwargs)
                    results.append(result)

                return ScenarioResult(config=config, results=results)
            finally:
                if token is not None:
                    _active_mock_registry.reset(token)

        return wrapper

    return decorator
