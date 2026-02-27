"""Continuous evaluation runner — sampling, alerting, and background eval loop."""

from __future__ import annotations

import asyncio
import logging
import os
import random
import urllib.request
from json import dumps as json_dumps
from typing import TYPE_CHECKING, Any

from attest._proto.types import Assertion, EvaluateBatchResult, Trace

_DEFAULT_QUEUE_SIZE: int = 1000


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


class AlertDispatcher:
    """Routes drift alert notifications to a generic webhook and/or Slack."""

    def __init__(
        self,
        webhook_url: str | None = None,
        slack_url: str | None = None,
    ) -> None:
        self._webhook_url = webhook_url
        self._slack_url = slack_url

    async def dispatch(self, alert: dict[str, Any]) -> None:
        """POST alert payload to configured endpoints.

        Sends JSON body to webhook_url (if set) and a formatted Slack message
        to slack_url (if set). Both posts run concurrently. Errors are logged
        but not raised so that alert failures never block evaluation.
        """
        coros: list[Any] = []
        loop = asyncio.get_running_loop()

        if self._webhook_url:
            coros.append(
                loop.run_in_executor(None, self._post_json, self._webhook_url, alert)
            )

        if self._slack_url:
            slack_payload = {"text": self._format_slack(alert)}
            coros.append(
                loop.run_in_executor(None, self._post_json, self._slack_url, slack_payload)
            )

        if coros:
            results = await asyncio.gather(*coros, return_exceptions=True)
            for result in results:
                if isinstance(result, Exception):
                    logger.warning("Alert dispatch failed: %s", result)

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
            with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
                resp.read()
        except Exception as exc:
            raise RuntimeError(f"POST to {url} failed: {exc}") from exc

    def _format_slack(self, alert: dict[str, Any]) -> str:
        """Format alert dict as a Slack-friendly text message."""
        drift_type = alert.get("drift_type", "unknown")
        score = alert.get("score", "n/a")
        trace_id = alert.get("trace_id", "n/a")
        return f"[attest] drift alert — type={drift_type} score={score} trace_id={trace_id}"


class ContinuousEvalRunner:
    """Asyncio background task for continuous evaluation of live traces."""

    def __init__(
        self,
        client: AttestClient,
        assertions: list[Assertion],
        sample_rate: float = 1.0,
        alert_webhook: str | None = None,
        alert_slack_url: str | None = None,
        maxsize: int | None = None,
    ) -> None:
        self._client = client
        self._assertions = assertions
        self._sampler = Sampler(sample_rate)
        self._dispatcher = AlertDispatcher(
            webhook_url=alert_webhook,
            slack_url=alert_slack_url,
        )
        resolved_maxsize = maxsize if maxsize is not None else _queue_maxsize()
        self._queue: asyncio.Queue[Trace] = asyncio.Queue(maxsize=resolved_maxsize)
        self._task: asyncio.Task[None] | None = None
        self._running = False

    async def evaluate_trace(self, trace: Trace) -> EvaluateBatchResult | None:
        """Evaluate a trace if it passes the sampler. Returns None if not sampled."""
        if not self._sampler.should_sample():
            return None
        return await self._client.evaluate_batch(trace, self._assertions)

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
        """Stop the background evaluation loop and drain the queue."""
        self._running = False
        if self._task is not None and not self._task.done():
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        self._task = None

    async def _loop(self) -> None:
        """Continuously dequeue and evaluate traces while running."""
        while self._running:
            try:
                trace = await asyncio.wait_for(self._queue.get(), timeout=1.0)
            except asyncio.TimeoutError:
                continue
            try:
                await self.evaluate_trace(trace)
            except Exception:
                logger.exception("Continuous eval failed for trace %s", trace.trace_id)
            finally:
                self._queue.task_done()
