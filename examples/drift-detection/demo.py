"""Drift detection demo — monitors agent behavior over time.

Demonstrates how ContinuousEvalRunner + result history can detect
when agent performance drifts from baseline. Uses ATTEST_SIMULATION=1
so no engine binary is needed.

Run:
    cd /path/to/attest
    PYTHONPATH=sdks/python/src python examples/drift-detection/demo.py
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys

os.environ["ATTEST_SIMULATION"] = "1"

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdks", "python", "src"))

from attest import Assertion, ManualAdapter, Trace  # noqa: E402
from attest.client import AttestClient  # noqa: E402
from attest.continuous import ContinuousEvalRunner  # noqa: E402
from attest.engine_manager import EngineManager  # noqa: E402

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
logger = logging.getLogger("drift-detection-demo")


def build_baseline_traces() -> list[Trace]:
    """Build traces representing normal (baseline) agent behavior."""
    adapter = ManualAdapter(agent_id="qa-agent")
    traces: list[Trace] = []

    normal_scenarios = [
        ("What is Python?", "Python is a programming language.", 50, 0.002, 300),
        ("Explain REST APIs", "REST APIs use HTTP methods.", 80, 0.003, 450),
        ("What is Docker?", "Docker is a containerization platform.", 60, 0.002, 350),
    ]

    for question, answer, tokens, cost, latency in normal_scenarios:
        trace = adapter.capture(lambda b, q=question, a=answer, t=tokens, c=cost, l=latency: (
            b.set_input(message=q),
            b.add_llm_call(
                name="gpt-4.1-mini",
                args={"model": "gpt-4.1-mini"},
                result={"completion": a, "total_tokens": t},
            ),
            b.set_output(message=a),
            b.set_metadata(total_tokens=t, cost_usd=c, latency_ms=l),
        ))
        traces.append(trace)

    return traces


def build_drifted_traces() -> list[Trace]:
    """Build traces that show drift — higher cost, more tokens, slower."""
    adapter = ManualAdapter(agent_id="qa-agent")
    traces: list[Trace] = []

    drifted_scenarios = [
        ("What is Python?", "Python is a...(verbose response)...", 500, 0.02, 3000),
        ("Explain REST APIs", "REST...(extremely long)...", 800, 0.035, 5000),
        ("What is Docker?", "Docker...(over-explaining)...", 600, 0.025, 4000),
    ]

    for question, answer, tokens, cost, latency in drifted_scenarios:
        trace = adapter.capture(lambda b, q=question, a=answer, t=tokens, c=cost, l=latency: (
            b.set_input(message=q),
            b.add_llm_call(
                name="gpt-4.1",
                args={"model": "gpt-4.1"},
                result={"completion": a, "total_tokens": t},
            ),
            b.set_output(message=a),
            b.set_metadata(total_tokens=t, cost_usd=c, latency_ms=l),
        ))
        traces.append(trace)

    return traces


async def main() -> None:
    """Run drift detection demo."""
    logger.info("Drift Detection Demo (simulation mode)")

    engine = EngineManager(engine_path="/dev/null")
    client = AttestClient(engine)

    assertions = [
        Assertion(
            assertion_id="cost-budget",
            type="constraint",
            spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.01},
        ),
        Assertion(
            assertion_id="token-budget",
            type="constraint",
            spec={"field": "metadata.total_tokens", "operator": "lte", "value": 200},
        ),
        Assertion(
            assertion_id="latency-sla",
            type="constraint",
            spec={"field": "metadata.latency_ms", "operator": "lte", "value": 1000},
        ),
    ]

    runner = ContinuousEvalRunner(
        client=client,
        assertions=assertions,
        sample_rate=1.0,
    )

    await runner.start()

    # Phase 1: Baseline traces (normal behavior)
    logger.info("--- Phase 1: Baseline (normal behavior) ---")
    baseline = build_baseline_traces()
    for i, trace in enumerate(baseline):
        await runner.submit(trace)
        logger.info(
            "Baseline trace %d: tokens=%d, cost=$%.3f, latency=%dms",
            i + 1,
            trace.metadata.total_tokens if trace.metadata else 0,
            trace.metadata.cost_usd if trace.metadata else 0,
            trace.metadata.latency_ms if trace.metadata else 0,
        )

    await asyncio.sleep(1)

    # Phase 2: Drifted traces (degraded behavior)
    logger.info("--- Phase 2: Drifted (degraded behavior) ---")
    drifted = build_drifted_traces()
    for i, trace in enumerate(drifted):
        await runner.submit(trace)
        logger.info(
            "Drifted trace %d: tokens=%d, cost=$%.3f, latency=%dms",
            i + 1,
            trace.metadata.total_tokens if trace.metadata else 0,
            trace.metadata.cost_usd if trace.metadata else 0,
            trace.metadata.latency_ms if trace.metadata else 0,
        )

    await asyncio.sleep(1)
    await runner.stop()

    # Compare baseline vs drifted metrics
    logger.info("--- Comparison ---")
    baseline_avg_tokens = sum(
        t.metadata.total_tokens for t in baseline if t.metadata
    ) / len(baseline)
    drifted_avg_tokens = sum(
        t.metadata.total_tokens for t in drifted if t.metadata
    ) / len(drifted)

    baseline_avg_cost = sum(
        t.metadata.cost_usd for t in baseline if t.metadata
    ) / len(baseline)
    drifted_avg_cost = sum(
        t.metadata.cost_usd for t in drifted if t.metadata
    ) / len(drifted)

    logger.info("Avg tokens: baseline=%.0f, drifted=%.0f (%.1fx increase)",
                baseline_avg_tokens, drifted_avg_tokens,
                drifted_avg_tokens / baseline_avg_tokens)
    logger.info("Avg cost: baseline=$%.4f, drifted=$%.4f (%.1fx increase)",
                baseline_avg_cost, drifted_avg_cost,
                drifted_avg_cost / baseline_avg_cost)
    logger.info("Drift detected: cost and token usage increased significantly")
    logger.info("Demo complete")


if __name__ == "__main__":
    asyncio.run(main())
