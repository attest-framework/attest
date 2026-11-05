"""Continuous evaluation runner — sampling, alerting, and background eval loop."""

from __future__ import annotations

import asyncio
import http.client
import logging
import os
import random
import socket
import threading
import time
import urllib.parse
from collections.abc import Callable
from dataclasses import replace
from json import dumps as json_dumps
from typing import TYPE_CHECKING, Any

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
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
_MAX_RESPONSE_BYTES: int = 4096
_EXECUTOR_GRACE_S: float = 0.5
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
        posts = []

        if self._webhook_url:
            posts.append(self._bounded_post(loop, self._webhook_url, alert))

        if self._slack_url:
            slack_payload = {"text": self._format_slack(alert)}
            posts.append(self._bounded_post(loop, self._slack_url, slack_payload))

        failures: list[Exception] = []
        for result in await asyncio.gather(*posts, return_exceptions=True):
            if isinstance(result, Exception):
                logger.warning("Alert dispatch failed: %s", result)
                failures.append(result)
        return failures

    async def _bounded_post(
        self, loop: asyncio.AbstractEventLoop, url: str, payload: dict[str, Any]
    ) -> None:
        """Run one POST in the executor; stop waiting shortly after the total deadline.

        ``_post_json`` enforces the deadline on the socket itself; this bound covers what it
        cannot interrupt (name resolution).
        """
        future = loop.run_in_executor(None, self._post_json, url, payload)
        try:
            await asyncio.wait_for(future, timeout=self._timeout_s + _EXECUTOR_GRACE_S)
        except asyncio.TimeoutError as exc:
            raise AlertDispatchError(f"POST to {url} timed out after {self._timeout_s}s") from exc

    def _post_json(self, url: str, payload: dict[str, Any]) -> None:
        """Synchronous HTTP POST of a JSON payload under one total deadline. Run in executor.

        The deadline covers connect, send, the status line, headers and the (at most
        ``_MAX_RESPONSE_BYTES``) body read: a watchdog shuts the socket down at expiry, so a
        receiver that trickles bytes cannot keep the dispatch alive. Any non-2xx status fails.
        """
        parts = urllib.parse.urlsplit(url)
        if parts.scheme not in ("http", "https") or not parts.hostname:
            raise AlertDispatchError(f"POST to {url} failed: not an http(s) URL")
        connection_class = (
            http.client.HTTPSConnection if parts.scheme == "https" else http.client.HTTPConnection
        )
        target = (parts.path or "/") + (f"?{parts.query}" if parts.query else "")
        deadline = time.monotonic() + self._timeout_s
        expired = threading.Event()
        conn = connection_class(parts.hostname, parts.port, timeout=self._timeout_s)

        connected: list[socket.socket] = []

        def abort() -> None:
            # Shut down the socket captured at connect time: for HTTP/1.0 and
            # ``Connection: close`` responses http.client detaches it from ``conn`` during
            # getresponse(), while the response keeps reading from it.
            expired.set()
            for sock in connected:
                try:
                    sock.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

        watchdog = threading.Timer(self._timeout_s, abort)
        watchdog.daemon = True
        watchdog.start()
        try:
            conn.connect()
            connected.append(conn.sock)
            if expired.is_set():
                abort()
            conn.request(
                "POST",
                target,
                body=json_dumps(payload).encode(),
                headers={"Content-Type": "application/json"},
            )
            resp = conn.getresponse()
            received = 0
            while received < _MAX_RESPONSE_BYTES:
                chunk = resp.read1(1024)
                if not chunk:
                    break
                received += len(chunk)
            if expired.is_set() or time.monotonic() >= deadline:
                raise TimeoutError
            if not 200 <= resp.status < 300:
                raise AlertDispatchError(f"POST to {url} failed: HTTP {resp.status}")
        except AlertDispatchError:
            raise
        except Exception as exc:
            if expired.is_set() or time.monotonic() >= deadline:
                raise AlertDispatchError(
                    f"POST to {url} timed out after {self._timeout_s}s"
                ) from exc
            raise AlertDispatchError(f"POST to {url} failed: {exc}") from exc
        finally:
            watchdog.cancel()
            conn.close()

    def _format_slack(self, alert: dict[str, Any]) -> str:
        """Render the alert fields as a Slack-friendly text message."""
        return (
            f"[attest] drift alert — runner={alert['runner']} "
            f"assertion={alert['assertion_id']} "
            f"status={alert['status']} score={alert['score']} "
            f"baseline_mean={alert['baseline_mean']} "
            f"baseline_count={alert['baseline_count']} "
            f"static_threshold={alert['static_threshold']} "
            f"trace_id={alert['trace_id']}"
        )


def _static_threshold(assertion: Assertion) -> float:
    """The assertion's static bar, resolved exactly like the engine.

    A numeric ``threshold`` above 0 is used as given; a threshold <= 0, a missing one, or
    ``"dynamic"`` resolves to 0.8 (``judge_eval.go`` and ``embedding_eval.go``), which is
    also the floor of the dynamic bar.
    """
    value = assertion.spec.get("threshold")
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0:
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
        # query_drift reads an assertion's latest history row, not the evaluated trace's, so
        # evaluate -> query_drift must not interleave across traces (loop and direct calls).
        self._evaluation_lock = asyncio.Lock()

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
        async with self._evaluation_lock:
            result = await self._client.evaluate_batch(trace, self._assertions)
            alerts = await self._collect_alerts(trace, result) if self._dispatcher.enabled else []
        for alert in alerts:
            await self._dispatch(alert)
        return result

    async def _collect_alerts(
        self, trace: Trace, result: EvaluateBatchResult
    ) -> list[dict[str, Any]]:
        """Read drift for each evaluated row; return the alert payloads. Call under the lock."""
        by_id = {a.assertion_id: a for a in self._assertions}
        rows = [
            (r, by_id[r.assertion_id])
            for r in result.results
            if r.assertion_id in by_id and r.status != STATUS_NOT_EVALUATED
        ]
        built = await asyncio.gather(*(self._build_alert(trace, r, a) for r, a in rows))
        return [alert for alert in built if alert is not None]

    async def _build_alert(
        self, trace: Trace, row: AssertionResult, assertion: Assertion
    ) -> dict[str, Any] | None:
        static = _static_threshold(assertion)
        try:
            drift = await self._read_drift(row.assertion_id, static)
        except Exception as exc:
            self._emit_error(exc)
            drift = None
        drifted = drift is not None and drift.status == STATUS_DRIFT_DETECTED
        hard_failed = row.status == STATUS_HARD_FAIL
        if not drifted and not hard_failed:
            return None
        return {
            "runner": self.name,
            "assertion_id": row.assertion_id,
            "status": STATUS_HARD_FAIL if hard_failed else STATUS_DRIFT_DETECTED,
            "result_status": row.status,
            "score": row.score,
            "baseline_mean": drift.mean if drift is not None else None,
            "baseline_count": drift.count if drift is not None else None,
            "static_threshold": static,
            "trace_id": trace.trace_id,
        }

    async def _dispatch(self, alert: dict[str, Any]) -> None:
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
