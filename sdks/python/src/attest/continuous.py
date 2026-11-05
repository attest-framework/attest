"""Continuous evaluation runner — sampling, alerting, and background eval loop."""

from __future__ import annotations

import asyncio
import logging
import os
import random
import urllib.request
from collections.abc import Callable
from dataclasses import replace
from json import dumps as json_dumps
from typing import TYPE_CHECKING, Any

from attest._proto.types import (
    STATUS_HARD_FAIL,
    Assertion,
    AssertionResult,
    DriftReport,
    EvaluateBatchResult,
    Trace,
)
from attest.assertion_id import OccurrenceCounter

_DEFAULT_QUEUE_SIZE: int = 1000
_DEFAULT_STATIC_THRESHOLD: float = 0.8
_DEFAULT_DISPATCH_TIMEOUT_S: float = 10.0
_DEFAULT_DRAIN_TIMEOUT_S: float = 30.0
STATUS_DRIFT_DETECTED: str = "drift_detected"


def _queue_maxsize() -> int:
    """Read queue bound from ATTEST_CONTINUOUS_QUEUE_SIZE env var.

    Returns the default (1000) when the variable is unset or unparseable.
    """
    raw = os.environ.get("ATTEST_CONTINUOUS_QUEUE_SIZE", "")
    if raw:
        try:
            return int(raw)
        except ValueError:
            logger.warning(
                "ATTEST_CONTINUOUS_QUEUE_SIZE=%r is not a valid int; using default %d",
                raw,
                _DEFAULT_QUEUE_SIZE,
            )
    return _DEFAULT_QUEUE_SIZE


if TYPE_CHECKING:
    from attest.client import AttestClient

logger = logging.getLogger("attest.continuous")


class Sampler:
    """Probabilistic filter based on a sample rate in [0.0, 1.0]."""

    def __init__(self, rate: float) -> None:
        if not 0.0 <= rate <= 1.0:
            raise ValueError(f"sample_rate must be in [0.0, 1.0], got {rate}")
        self._rate = rate

    def should_sample(self) -> bool:
        """Return True with probability equal to the configured rate."""
        return random.random() < self._rate


class AlertDispatchError(RuntimeError):
    """An alert POST failed: the receiver was down, timed out, or answered with an error."""


class AlertDispatcher:
    """Routes drift alert notifications to a generic webhook and/or Slack."""

    def __init__(
        self,
        webhook_url: str | None = None,
        slack_url: str | None = None,
        timeout_s: float = _DEFAULT_DISPATCH_TIMEOUT_S,
    ) -> None:
        if timeout_s <= 0:
            raise ValueError(f"timeout_s must be > 0, got {timeout_s}")
        self._webhook_url = webhook_url
        self._slack_url = slack_url
        self._timeout_s = timeout_s

    @property
    def enabled(self) -> bool:
        """True when at least one destination is configured."""
        return bool(self._webhook_url or self._slack_url)

    async def dispatch(self, alert: dict[str, Any]) -> list[Exception]:
        """POST the alert to every configured destination, each bounded by the timeout.

        Sends the JSON body to ``webhook_url`` and a Slack incoming-webhook message to
        ``slack_url`` concurrently. A failed destination never raises and never blocks the
        other one; its error is logged and returned so the caller can surface it.
        """
        loop = asyncio.get_running_loop()
        futures: list[asyncio.Future[None]] = []

        if self._webhook_url:
            futures.append(loop.run_in_executor(None, self._post_json, self._webhook_url, alert))

        if self._slack_url:
            slack_payload = {"text": self._format_slack(alert)}
            futures.append(
                loop.run_in_executor(None, self._post_json, self._slack_url, slack_payload)
            )

        failures: list[Exception] = []
        for result in await asyncio.gather(*futures, return_exceptions=True):
            if isinstance(result, Exception):
                logger.warning("Alert dispatch failed: %s", result)
                failures.append(result)
        return failures

    def _post_json(self, url: str, payload: dict[str, Any]) -> None:
        """Synchronous HTTP POST of JSON payload. Run in executor."""
        body = json_dumps(payload).encode()
        req = urllib.request.Request(
            url,
            data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self._timeout_s) as resp:  # noqa: S310
                resp.read()
        except Exception as exc:
            raise AlertDispatchError(f"POST to {url} failed: {exc}") from exc

    def _format_slack(self, alert: dict[str, Any]) -> str:
        """Format alert dict as a Slack-friendly text message."""
        return (
            f"[attest] drift alert — runner={alert.get('runner', 'n/a')} "
            f"assertion={alert.get('assertion_id', 'n/a')} "
            f"status={alert.get('status', 'n/a')} score={alert.get('score', 'n/a')} "
            f"baseline_mean={alert.get('baseline_mean', 'n/a')} "
            f"baseline_count={alert.get('baseline_count', 'n/a')} "
            f"static_threshold={alert.get('static_threshold', 'n/a')} "
            f"trace_id={alert.get('trace_id', 'n/a')}"
        )


def _static_threshold(assertion: Assertion) -> float:
    """The assertion's static bar: its numeric ``threshold``, else the 0.8 default.

    A ``"dynamic"`` or absent threshold falls back to the engine default, which is also the
    floor of the dynamic bar.
    """
    value = assertion.spec.get("threshold")
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return _DEFAULT_STATIC_THRESHOLD
    return float(value)


class ContinuousEvalRunner:
    """Asyncio background task for continuous evaluation of live traces.

    ``name`` is the scope of the assertion ids (``a2_...`` over the name, type, spec and
    occurrence): keep it stable across restarts so history and baselines keep matching, and
    distinct between runners.
    """

    def __init__(
        self,
        client: AttestClient,
        assertions: list[Assertion],
        sample_rate: float = 1.0,
        alert_webhook: str | None = None,
        alert_slack_url: str | None = None,
        maxsize: int | None = None,
        *,
        name: str,
        on_error: Callable[[Exception], None] | None = None,
        dispatch_timeout_s: float = _DEFAULT_DISPATCH_TIMEOUT_S,
        drain_timeout_s: float = _DEFAULT_DRAIN_TIMEOUT_S,
    ) -> None:
        """Create a runner.

        ``on_error`` receives every drift-read or alert-dispatch failure (and any evaluation
        failure in the background loop); each is also logged. ``dispatch_timeout_s`` bounds
        each alert POST and ``drain_timeout_s`` bounds how long ``stop()`` waits for queued
        and in-flight work.
        """
        if drain_timeout_s <= 0:
            raise ValueError(f"drain_timeout_s must be > 0, got {drain_timeout_s}")
        self.name = name
        self._client = client
        # Ids are scoped to the runner name; the same list is evaluated against every trace.
        counter = OccurrenceCounter(name)
        self._assertions = [replace(a) for a in assertions]
        for assertion in self._assertions:
            counter.assign(assertion)
        self._sampler = Sampler(sample_rate)
        self._dispatcher = AlertDispatcher(
            webhook_url=alert_webhook,
            slack_url=alert_slack_url,
            timeout_s=dispatch_timeout_s,
        )
        self._on_error = on_error
        self._drain_timeout_s = drain_timeout_s
        resolved_maxsize = maxsize if maxsize is not None else _queue_maxsize()
        self._queue: asyncio.Queue[Trace] = asyncio.Queue(maxsize=resolved_maxsize)
        self._task: asyncio.Task[None] | None = None
        self._running = False

    def _emit_error(self, exc: Exception) -> None:
        """Log the error and hand it to ``on_error``; a failing callback is logged, not raised."""
        logger.error("ContinuousEvalRunner %s: %s", self.name, exc)
        if self._on_error is None:
            return
        try:
            self._on_error(exc)
        except Exception:
            logger.exception("ContinuousEvalRunner %s: on_error callback raised", self.name)

    async def evaluate_trace(self, trace: Trace) -> EvaluateBatchResult | None:
        """Evaluate a trace if it passes the sampler. Returns None if not sampled.

        With an alert destination configured, each assertion's drift is then read and an alert
        is dispatched for every ``drift_detected`` or ``hard_fail``. Drift-read and dispatch
        failures are emitted through ``on_error``; they never fail the evaluation.
        """
        if not self._sampler.should_sample():
            return None
        result = await self._client.evaluate_batch(trace, self._assertions)
        if self._dispatcher.enabled:
            await self._alert(trace, result)
        return result

    async def _alert(self, trace: Trace, result: EvaluateBatchResult) -> None:
        by_id = {a.assertion_id: a for a in self._assertions}
        rows = [(r, by_id[r.assertion_id]) for r in result.results if r.assertion_id in by_id]
        await asyncio.gather(*(self._alert_row(trace, r, a) for r, a in rows))

    async def _alert_row(self, trace: Trace, row: AssertionResult, assertion: Assertion) -> None:
        static = _static_threshold(assertion)
        try:
            drift = await self._read_drift(row.assertion_id, static)
        except Exception as exc:
            self._emit_error(exc)
            drift = None
        drifted = drift is not None and drift.status == STATUS_DRIFT_DETECTED
        if not drifted and row.status != STATUS_HARD_FAIL:
            return
        alert: dict[str, Any] = {
            "runner": self.name,
            "trace_id": trace.trace_id,
            "assertion_id": row.assertion_id,
            "status": STATUS_DRIFT_DETECTED if drifted else STATUS_HARD_FAIL,
            "result_status": row.status,
            "score": row.score,
            "baseline_mean": drift.mean if drift is not None else None,
            "baseline_count": drift.count if drift is not None else None,
            "static_threshold": static,
        }
        try:
            for failure in await self._dispatcher.dispatch(alert):
                self._emit_error(failure)
        except Exception as exc:
            self._emit_error(exc)

    async def _read_drift(self, assertion_id: str, static_threshold: float) -> DriftReport | None:
        from attest.config import is_simulation_mode

        if is_simulation_mode():
            return None
        raw = await self._client.send_request(
            "query_drift",
            {"assertion_id": assertion_id, "static_threshold": static_threshold},
        )
        return DriftReport.from_dict(raw["report"])

    async def submit(self, trace: Trace) -> None:
        """Enqueue a trace for background evaluation.

        If the queue is at capacity the trace is dropped and a warning is
        logged. This prevents unbounded memory growth under high submission
        rates — increase ``ATTEST_CONTINUOUS_QUEUE_SIZE`` or the ``maxsize``
        constructor argument to raise the limit.
        """
        try:
            self._queue.put_nowait(trace)
        except asyncio.QueueFull:
            logger.warning(
                "ContinuousEvalRunner queue full (maxsize=%d); dropping trace %s",
                self._queue.maxsize,
                trace.trace_id,
            )

    async def start(self) -> None:
        """Start the background evaluation loop."""
        if self._running:
            return
        self._running = True
        self._task = asyncio.create_task(self._loop(), name="attest-continuous-runner")

    async def stop(self) -> None:
        """Stop the loop after queued and in-flight evaluations and alerts finish.

        Waits at most ``drain_timeout_s``; work still pending then is cancelled and the
        timeout is emitted through ``on_error``.
        """
        self._running = False
        task, self._task = self._task, None
        if task is None:
            return
        try:
            await asyncio.wait_for(self._queue.join(), timeout=self._drain_timeout_s)
        except asyncio.TimeoutError:
            self._emit_error(
                TimeoutError(
                    f"stop() drain exceeded {self._drain_timeout_s}s; cancelling pending work"
                )
            )
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass

    async def _loop(self) -> None:
        """Dequeue and evaluate traces until cancelled by ``stop()``."""
        while True:
            trace = await self._queue.get()
            try:
                await self.evaluate_trace(trace)
            except Exception as exc:
                self._emit_error(
                    RuntimeError(f"evaluation failed for trace {trace.trace_id}: {exc}")
                )
            finally:
                self._queue.task_done()
