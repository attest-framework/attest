"""Continuous evaluation demo — runs without an engine binary.

Uses ATTEST_SIMULATION=1 so evaluate_batch() returns deterministic pass
results. Demonstrates the ContinuousEvalRunner pipeline: sampling,
queuing, background evaluation, and result collection.

Run:
    cd /path/to/attest
    PYTHONPATH=sdks/python/src uv run python examples/continuous-eval/demo.py
"""

from __future__ import annotations

import asyncio
import logging
import os
import sys

# Enable simulation mode — no engine binary needed
os.environ["ATTEST_SIMULATION"] = "1"

# Ensure SDK is importable when running from repo root
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdks", "python", "src"))

from attest import Assertion, ManualAdapter, Trace  # noqa: E402
from attest.client import AttestClient  # noqa: E402
from attest.continuous import ContinuousEvalRunner  # noqa: E402
from attest.engine_manager import EngineManager  # noqa: E402

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
logger = logging.getLogger("continuous-eval-demo")


def build_traces() -> list[Trace]:
    """Build 5 sample traces with varying characteristics."""
    adapter = ManualAdapter(agent_id="demo-agent")
    traces: list[Trace] = []

    scenarios = [
        ("What is 2+2?", "4", 10, 0.001),
        ("Explain quantum computing", "Quantum computing uses qubits...", 500, 0.05),
        ("Translate hello to French", "Bonjour", 25, 0.002),
        ("Write a haiku about AI", "Silicon dreams flow\nThrough circuits of endless thought\nMachines learn to feel", 80, 0.008),
        ("Summarize the news", "Today's headlines include...", 200, 0.02),
    ]

    for question, answer, tokens, cost in scenarios:
        trace = adapter.capture(lambda b, q=question, a=answer, t=tokens, c=cost: (
            b.set_input(message=q),
            b.add_llm_call(
                name="gpt-4.1",
                args={"model": "gpt-4.1"},
                result={"completion": a, "total_tokens": t},
            ),
            b.set_output(message=a),
            b.set_metadata(total_tokens=t, cost_usd=c),
        ))
        traces.append(trace)

    return traces


def build_assertions() -> list[Assertion]:
    """Define assertions for continuous evaluation."""
    return [
        Assertion(
            assertion_id="cost-check",
            type="constraint",
            spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.10},
        ),
        Assertion(
            assertion_id="output-check",
            type="content",
            spec={"check": "non_empty"},
        ),
    ]


async def main() -> None:
    """Run the continuous evaluation demo."""
    logger.info("Starting continuous eval demo (simulation mode)")

    # Create client — engine never started in simulation mode
    engine = EngineManager(engine_path="/dev/null")
    client = AttestClient(engine)

    traces = build_traces()
    assertions = build_assertions()

    logger.info("Built %d traces and %d assertions", len(traces), len(assertions))

    # Create runner with 100% sample rate
    runner = ContinuousEvalRunner(
        client=client,
        assertions=assertions,
        sample_rate=1.0,
    )

    # Start background loop
    await runner.start()
    logger.info("Runner started — submitting traces")

    # Submit all traces
    for i, trace in enumerate(traces):
        await runner.submit(trace)
        logger.info("Submitted trace %d/%d (id=%s)", i + 1, len(traces), trace.trace_id)

    # Wait for queue to drain
    logger.info("Waiting for evaluation queue to drain...")
    await asyncio.sleep(2)

    # Stop runner
    await runner.stop()
    logger.info("Runner stopped")

    # Direct evaluation to show results
    logger.info("Running direct evaluation on each trace:")
    for i, trace in enumerate(traces):
        result = await client.evaluate_batch(trace, assertions)
        status_line = ", ".join(
            f"{r.assertion_id}={r.status}" for r in result.results
        )
        logger.info(
            "  Trace %d: %s (cost=%.4f, duration=%dms)",
            i + 1,
            status_line,
            result.total_cost,
            result.total_duration_ms,
        )

    logger.info("Demo complete — all traces evaluated in simulation mode")


if __name__ == "__main__":
    asyncio.run(main())
