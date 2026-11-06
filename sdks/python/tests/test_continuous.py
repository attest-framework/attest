"""Tests for the continuous evaluation runner."""

from __future__ import annotations

import asyncio
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest._proto.types import Assertion, AssertionResult, EvaluateBatchResult, Trace, Usage
from attest.assertion_id import compute_assertion_id
from attest.continuous import AlertDispatcher, AlertDispatchError, ContinuousEvalRunner, Sampler

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_trace(trace_id: str = "t-001") -> Trace:
    return Trace(trace_id=trace_id, output={"message": "hello"})


def _make_assertion() -> Assertion:
    return Assertion(
        assertion_id="a-001",
        type="constraint",
        spec={"field": "metadata.cost_usd", "operator": "lte", "value": 1.0},
    )


def _make_batch_result() -> EvaluateBatchResult:
    return EvaluateBatchResult(
        results=[
            AssertionResult(
                assertion_id="a-001",
                status="pass",
                score=1.0,
                explanation="ok",
            )
        ],
        usage=Usage(),
        total_cost=0.0,
        total_duration_ms=0,
    )


# ---------------------------------------------------------------------------
# Sampler tests
# ---------------------------------------------------------------------------


class TestSampler:
    def test_rate_one_always_samples(self) -> None:
        sampler = Sampler(1.0)
        assert all(sampler.should_sample() for _ in range(100))

    def test_rate_zero_never_samples(self) -> None:
        sampler = Sampler(0.0)
        assert not any(sampler.should_sample() for _ in range(100))

    def test_invalid_rate_raises(self) -> None:
        with pytest.raises(ValueError):
            Sampler(1.5)

    def test_invalid_negative_rate_raises(self) -> None:
        with pytest.raises(ValueError):
            Sampler(-0.1)

    def test_rate_half_is_probabilistic(self) -> None:
        sampler = Sampler(0.5)
        results = [sampler.should_sample() for _ in range(1000)]
        # With 1000 trials at p=0.5, should have both True and False
        assert True in results
        assert False in results


# ---------------------------------------------------------------------------
# AlertDispatcher tests
# ---------------------------------------------------------------------------


class TestAlertDispatcher:
    def test_dispatch_no_urls_is_noop(self) -> None:
        dispatcher = AlertDispatcher()
        # No error when both URLs are None
        asyncio.run(dispatcher.dispatch({"drift_type": "cosine", "score": 0.3}))

    def test_dispatch_posts_to_webhook(self) -> None:
        dispatcher = AlertDispatcher(webhook_url="https://hooks.example.com/alert")
        alert = {"drift_type": "cosine", "score": 0.3, "trace_id": "t-1"}

        with patch.object(dispatcher, "_post_json") as mock_post:
            asyncio.run(dispatcher.dispatch(alert))
            mock_post.assert_called_once_with("https://hooks.example.com/alert", alert)

    def test_dispatch_posts_to_slack(self) -> None:
        dispatcher = AlertDispatcher(slack_url="https://hooks.slack.com/services/XXX")
        alert = {
            "runner": "m",
            "assertion_id": "a2_x",
            "status": "hard_fail",
            "result_status": "hard_fail",
            "score": 0.3,
            "baseline_mean": None,
            "baseline_count": None,
            "static_threshold": 0.8,
            "trace_id": "t-1",
        }

        with patch.object(dispatcher, "_post_json") as mock_post:
            asyncio.run(dispatcher.dispatch(alert))
            call_args = mock_post.call_args
            assert call_args[0][0] == "https://hooks.slack.com/services/XXX"
            assert "text" in call_args[0][1]

    def test_dispatch_posts_to_both_urls(self) -> None:
        dispatcher = AlertDispatcher(
            webhook_url="https://hooks.example.com/w",
            slack_url="https://hooks.slack.com/s",
        )
        alert = {
            "runner": "m",
            "assertion_id": "a2_x",
            "status": "hard_fail",
            "result_status": "hard_fail",
            "score": 0.3,
            "baseline_mean": None,
            "baseline_count": None,
            "static_threshold": 0.8,
            "trace_id": "t-1",
        }

        with patch.object(dispatcher, "_post_json") as mock_post:
            asyncio.run(dispatcher.dispatch(alert))
            assert mock_post.call_count == 2

    def test_dispatch_logs_error_but_does_not_raise(self) -> None:
        dispatcher = AlertDispatcher(webhook_url="https://hooks.example.com/w")
        alert = {"drift_type": "cosine"}

        with patch.object(
            dispatcher,
            "_post_json",
            side_effect=RuntimeError("connection refused"),
        ):
            # Should not raise
            asyncio.run(dispatcher.dispatch(alert))

    def test_slack_message_format(self) -> None:
        dispatcher = AlertDispatcher()
        msg = dispatcher._format_slack(
            {
                "runner": "refund-monitor",
                "assertion_id": "a2_abc",
                "status": "drift_detected",
                "score": 0.42,
                "baseline_mean": 0.9,
                "baseline_count": 10,
                "static_threshold": 0.8,
                "trace_id": "t-99",
            }
        )
        for part in (
            "refund-monitor",
            "a2_abc",
            "drift_detected",
            "0.42",
            "0.9",
            "10",
            "0.8",
            "t-99",
        ):
            assert part in msg

    def test_dispatch_returns_the_failures(self) -> None:
        dispatcher = AlertDispatcher(webhook_url="https://hooks.example.com/w")
        failure = RuntimeError("connection refused")

        with patch.object(dispatcher, "_post_json", side_effect=failure):
            failures = asyncio.run(dispatcher.dispatch({"status": "hard_fail"}))

        assert failures == [failure]

    def test_non_positive_timeout_raises(self) -> None:
        with pytest.raises(ValueError):
            AlertDispatcher(webhook_url="https://hooks.example.com/w", timeout_s=0)


# ---------------------------------------------------------------------------
# ContinuousEvalRunner tests
# ---------------------------------------------------------------------------


class TestContinuousEvalRunner:
    def _make_runner(
        self,
        sample_rate: float = 1.0,
        evaluate_batch_return: EvaluateBatchResult | None = None,
    ) -> tuple[ContinuousEvalRunner, MagicMock]:
        client = MagicMock()
        client.evaluate_batch = AsyncMock(
            return_value=evaluate_batch_return or _make_batch_result()
        )
        runner = ContinuousEvalRunner(
            client=client,
            assertions=[_make_assertion()],
            name="test-runner",
            sample_rate=sample_rate,
        )
        return runner, client

    def test_evaluate_trace_returns_none_when_not_sampled(self) -> None:
        runner, client = self._make_runner(sample_rate=0.0)
        result = asyncio.run(runner.evaluate_trace(_make_trace()))
        assert result is None
        client.evaluate_batch.assert_not_awaited()

    def test_evaluate_trace_calls_client_when_sampled(self) -> None:
        runner, client = self._make_runner(sample_rate=1.0)
        result = asyncio.run(runner.evaluate_trace(_make_trace()))
        assert result is not None
        client.evaluate_batch.assert_awaited_once()

    def test_evaluate_trace_returns_batch_result(self) -> None:
        expected = _make_batch_result()
        runner, _ = self._make_runner(sample_rate=1.0, evaluate_batch_return=expected)
        result = asyncio.run(runner.evaluate_trace(_make_trace()))
        assert result is expected

    def test_start_and_stop(self) -> None:
        runner, _ = self._make_runner()

        async def _run() -> None:
            await runner.start()
            assert runner._running is True
            await runner.stop()
            assert runner._running is False

        asyncio.run(_run())

    def test_start_is_idempotent(self) -> None:
        runner, _ = self._make_runner()

        async def _run() -> None:
            await runner.start()
            task_before = runner._task
            await runner.start()
            assert runner._task is task_before
            await runner.stop()

        asyncio.run(_run())

    def test_submit_enqueues_trace(self) -> None:
        runner, _ = self._make_runner(sample_rate=0.0)

        async def _run() -> None:
            trace = _make_trace()
            await runner.submit(trace)
            assert runner._queue.qsize() == 1

        asyncio.run(_run())

    # -----------------------------------------------------------------------
    # P7 — Bounded queue
    # -----------------------------------------------------------------------

    def test_queue_default_maxsize_is_1000(self) -> None:
        runner, _ = self._make_runner()
        assert runner._queue.maxsize == 1000

    def test_queue_custom_maxsize(self) -> None:
        client = MagicMock()
        client.evaluate_batch = AsyncMock(return_value=_make_batch_result())
        runner = ContinuousEvalRunner(
            client=client,
            assertions=[_make_assertion()],
            name="test-runner",
            maxsize=50,
        )
        assert runner._queue.maxsize == 50

    def test_queue_maxsize_from_env(self) -> None:
        from unittest.mock import patch

        client = MagicMock()
        client.evaluate_batch = AsyncMock(return_value=_make_batch_result())

        with patch.dict("os.environ", {"ATTEST_CONTINUOUS_QUEUE_SIZE": "200"}):
            runner = ContinuousEvalRunner(
                client=client,
                assertions=[_make_assertion()],
                name="test-runner",
            )
        assert runner._queue.maxsize == 200

    def test_submit_drops_trace_when_queue_full(self) -> None:
        """submit() drops excess traces and logs a warning instead of blocking."""
        client = MagicMock()
        client.evaluate_batch = AsyncMock(return_value=_make_batch_result())
        runner = ContinuousEvalRunner(
            client=client,
            assertions=[_make_assertion()],
            name="test-runner",
            maxsize=2,
        )

        async def _run() -> None:
            # Fill the queue exactly
            await runner.submit(_make_trace("t-1"))
            await runner.submit(_make_trace("t-2"))
            assert runner._queue.qsize() == 2

            # Third submit should drop silently (no exception)
            await runner.submit(_make_trace("t-3"))
            # Queue size stays at 2
            assert runner._queue.qsize() == 2

        asyncio.run(_run())


class TestContinuousEvalRunnerIds:
    """Outside tests the id scope is the runner name (contract C3)."""

    @staticmethod
    def _runner(name: str, assertions: list[Assertion]) -> ContinuousEvalRunner:
        return ContinuousEvalRunner(client=MagicMock(), assertions=assertions, name=name)

    def test_ids_are_scoped_to_the_runner_name(self) -> None:
        runner = self._runner("refund-monitor", [_make_assertion()])
        (assertion,) = runner._assertions

        assert assertion.scope_id == "refund-monitor"
        assert assertion.assertion_id == compute_assertion_id(
            "refund-monitor", "constraint", _make_assertion().spec, 0
        )

    def test_same_assertions_under_another_name_get_other_ids(self) -> None:
        first = self._runner("monitor-a", [_make_assertion()])._assertions[0].assertion_id
        second = self._runner("monitor-b", [_make_assertion()])._assertions[0].assertion_id

        assert first != second

    def test_identical_assertions_get_distinct_ids(self) -> None:
        runner = self._runner("monitor", [_make_assertion(), _make_assertion()])

        first, second = (a.assertion_id for a in runner._assertions)

        assert first != second

    def test_caller_assertions_are_not_mutated(self) -> None:
        original = _make_assertion()

        self._runner("monitor", [original])

        assert original.assertion_id == "a-001"
        assert original.scope_id is None

    def test_evaluate_trace_sends_the_scoped_assertions(self) -> None:
        client = MagicMock()
        client.evaluate_batch = AsyncMock(return_value=_make_batch_result())
        runner = ContinuousEvalRunner(client=client, assertions=[_make_assertion()], name="m")

        asyncio.run(runner.evaluate_trace(_make_trace("t-1")))

        sent = client.evaluate_batch.call_args.args[1]
        assert [(a.scope_id, a.assertion_id) for a in sent] == [
            (a.scope_id, a.assertion_id) for a in runner._assertions
        ]


class TestDriftAlerts:
    """The runner reads drift after each evaluation and dispatches alerts (fake client)."""

    @staticmethod
    def _assertion(**spec: object) -> Assertion:
        return Assertion(assertion_id="x", type="llm_judge", spec={"target": "o", **spec})

    @staticmethod
    def _client(status: str, drift_status: str) -> tuple[MagicMock, list[dict[str, object]]]:
        queries: list[dict[str, object]] = []
        client = MagicMock()

        async def evaluate_batch(trace: Trace, assertions: list[Assertion]) -> EvaluateBatchResult:
            return EvaluateBatchResult(
                results=[
                    AssertionResult(
                        assertion_id=a.assertion_id, status=status, score=0.5, explanation="e"
                    )
                    for a in assertions
                ],
                usage=Usage(),
            )

        async def send_request(method: str, params: dict[str, object]) -> dict[str, object]:
            assert method == "query_drift"
            queries.append(params)
            return {
                "report": {
                    "assertion_id": params["assertion_id"],
                    "mean": 0.9,
                    "stddev": 0.0,
                    "count": 10,
                    "latest_score": 0.5,
                    "deviation": 4.0,
                    "status": drift_status,
                },
                "static_threshold": params["static_threshold"],
            }

        client.evaluate_batch = evaluate_batch
        client.send_request = send_request
        return client, queries

    @staticmethod
    def _runner(
        client: MagicMock, assertion: Assertion, **kwargs: object
    ) -> tuple[ContinuousEvalRunner, list[dict[str, object]]]:
        sent: list[dict[str, object]] = []

        runner = ContinuousEvalRunner(
            client=client,
            assertions=[assertion],
            name="m",
            sample_rate=1.0,
            **kwargs,  # type: ignore[arg-type]
        )
        runner._dispatcher.dispatch = AsyncMock(  # type: ignore[method-assign]
            side_effect=lambda alert: sent.append(alert) or []
        )
        return runner, sent

    def test_drift_detected_dispatches_with_the_resolved_static_threshold(self) -> None:
        client, queries = self._client("pass", "drift_detected")
        runner, sent = self._runner(
            client, self._assertion(threshold=0.6), alert_webhook="http://x"
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        (assertion,) = runner._assertions
        assert queries == [{"assertion_id": assertion.assertion_id, "static_threshold": 0.6}]
        (alert,) = sent
        assert alert["status"] == "drift_detected"
        assert alert["static_threshold"] == 0.6
        assert alert["baseline_count"] == 10

    def test_dynamic_threshold_queries_with_the_default_bar(self) -> None:
        client, queries = self._client("pass", "ok")
        runner, sent = self._runner(
            client, self._assertion(threshold="dynamic"), alert_webhook="http://x"
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert queries[0]["static_threshold"] == 0.8
        assert sent == []

    def test_hard_fail_without_drift_still_alerts(self) -> None:
        client, _ = self._client("hard_fail", "ok")
        runner, sent = self._runner(client, self._assertion(), alert_webhook="http://x")

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert [a["status"] for a in sent] == ["hard_fail"]

    def test_no_destination_never_reads_drift(self) -> None:
        client, queries = self._client("hard_fail", "drift_detected")
        runner, sent = self._runner(client, self._assertion())

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert queries == []
        assert sent == []

    def test_drift_read_failure_is_emitted_and_hard_fail_still_alerts(self) -> None:
        client, _ = self._client("hard_fail", "ok")
        client.send_request = AsyncMock(side_effect=RuntimeError("engine gone"))
        errors: list[Exception] = []
        runner, sent = self._runner(
            client, self._assertion(), alert_webhook="http://x", on_error=errors.append
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert [str(e) for e in errors] == ["engine gone"]
        assert [a["baseline_mean"] for a in sent] == [None]

    def test_raising_on_error_callback_does_not_crash_the_runner(self) -> None:
        client, _ = self._client("hard_fail", "ok")
        client.send_request = AsyncMock(side_effect=RuntimeError("engine gone"))

        def boom(exc: Exception) -> None:
            raise ValueError("callback bug")

        runner, _ = self._runner(client, self._assertion(), alert_webhook="http://x", on_error=boom)

        assert asyncio.run(runner.evaluate_trace(_make_trace())) is not None

    def test_stop_emits_a_timeout_when_the_drain_exceeds_the_bound(self) -> None:
        client, _ = self._client("pass", "ok")
        errors: list[Exception] = []

        async def slow(trace: Trace, assertions: list[Assertion]) -> EvaluateBatchResult:
            await asyncio.sleep(30)
            raise AssertionError("unreachable")

        client.evaluate_batch = slow
        runner, _ = self._runner(
            client, self._assertion(), on_error=errors.append, drain_timeout_s=0.2
        )

        async def scenario() -> None:
            await runner.start()
            await runner.submit(_make_trace())
            await asyncio.sleep(0.05)
            await runner.stop()

        asyncio.run(scenario())

        assert len(errors) == 1
        assert isinstance(errors[0], TimeoutError)

    def test_stop_timeout_discards_the_whole_queue_and_lets_in_flight_finish(self) -> None:
        client, _ = self._client("pass", "ok")
        errors: list[Exception] = []
        evaluated: list[str] = []
        inner = client.evaluate_batch

        async def slow(trace: Trace, assertions: list[Assertion]) -> EvaluateBatchResult:
            await asyncio.sleep(0.08)
            evaluated.append(trace.trace_id)
            return await inner(trace, assertions)

        client.evaluate_batch = slow
        runner, _ = self._runner(
            client, self._assertion(), on_error=errors.append, drain_timeout_s=0.02
        )

        async def scenario() -> None:
            await runner.start()
            for trace_id in ("t1", "t2", "t3", "t4"):
                await runner.submit(_make_trace(trace_id))
            await asyncio.sleep(0.01)
            await runner.stop()
            assert runner._queue.qsize() == 0
            await asyncio.sleep(0.2)
            assert evaluated == ["t1"]
            await runner.start()
            await asyncio.sleep(0.2)
            await runner.stop()

        asyncio.run(scenario())

        assert len(errors) == 1
        assert isinstance(errors[0], TimeoutError)
        assert "discarded 3 queued" in str(errors[0])
        assert evaluated == ["t1"]


_PAYLOAD_KEYS = {
    "runner",
    "assertion_id",
    "status",
    "result_status",
    "score",
    "baseline_mean",
    "baseline_count",
    "static_threshold",
    "trace_id",
}


class TestAlertPayload:
    """The one alert schema shared by both SDK runners."""

    def test_drifting_pass_payload_has_exactly_the_schema_keys_and_values(self) -> None:
        client, _ = TestDriftAlerts._client("pass", "drift_detected")
        runner, sent = TestDriftAlerts._runner(
            client, TestDriftAlerts._assertion(threshold=0.6), alert_webhook="http://x"
        )

        asyncio.run(runner.evaluate_trace(_make_trace("t-7")))

        (alert,) = sent
        assert set(alert) == _PAYLOAD_KEYS
        assert alert == {
            "runner": "m",
            "assertion_id": runner._assertions[0].assertion_id,
            "status": "drift_detected",
            "result_status": "pass",
            "score": 0.5,
            "baseline_mean": 0.9,
            "baseline_count": 10,
            "static_threshold": 0.6,
            "trace_id": "t-7",
        }

    def test_hard_fail_payload_has_the_schema_keys_and_nulls_when_the_drift_read_failed(
        self,
    ) -> None:
        client, _ = TestDriftAlerts._client("hard_fail", "ok")
        client.send_request = AsyncMock(side_effect=RuntimeError("engine gone"))
        runner, sent = TestDriftAlerts._runner(
            client,
            TestDriftAlerts._assertion(threshold="dynamic"),
            alert_webhook="http://x",
            on_error=lambda exc: None,
        )

        asyncio.run(runner.evaluate_trace(_make_trace("t-8")))

        (alert,) = sent
        assert alert == {
            "runner": "m",
            "assertion_id": runner._assertions[0].assertion_id,
            "status": "hard_fail",
            "result_status": "hard_fail",
            "score": 0.5,
            "baseline_mean": None,
            "baseline_count": None,
            "static_threshold": 0.8,
            "trace_id": "t-8",
        }

    def test_hard_fail_with_drift_reports_hard_fail(self) -> None:
        client, _ = TestDriftAlerts._client("hard_fail", "drift_detected")
        runner, sent = TestDriftAlerts._runner(
            client, TestDriftAlerts._assertion(), alert_webhook="http://x"
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        (alert,) = sent
        assert (alert["status"], alert["baseline_count"]) == ("hard_fail", 10)

    def test_not_evaluated_rows_are_neither_queried_nor_alerted(self) -> None:
        client, queries = TestDriftAlerts._client("not_evaluated", "drift_detected")
        runner, sent = TestDriftAlerts._runner(
            client, TestDriftAlerts._assertion(), alert_webhook="http://x"
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert queries == []
        assert sent == []

    def test_slack_text_renders_the_same_fields(self) -> None:
        alert = {
            "runner": "m",
            "assertion_id": "a2_x",
            "status": "drift_detected",
            "result_status": "pass",
            "score": None,
            "baseline_mean": None,
            "baseline_count": None,
            "static_threshold": 0.8,
            "trace_id": None,
        }

        text = AlertDispatcher()._format_slack(alert)

        assert text == (
            "[attest] drift alert — runner=m assertion=a2_x status=drift_detected score=None "
            "baseline_mean=None baseline_count=None static_threshold=0.8 trace_id=None"
        )


class TestStaticThresholdResolution:
    @pytest.mark.parametrize(
        ("threshold", "expected"),
        [
            (0, 0.8),
            (0.0, 0.8),
            (-0.5, 0.8),
            ("dynamic", 0.8),
            (None, 0.8),
            (True, 0.8),
            (0.6, 0.6),
            (1, 1.0),
        ],
    )
    def test_threshold_resolves_like_the_engine(self, threshold: object, expected: float) -> None:
        spec: dict[str, object] = {"target": "o"}
        if threshold is not None:
            spec["threshold"] = threshold
        client, queries = TestDriftAlerts._client("pass", "ok")
        runner, _ = TestDriftAlerts._runner(
            client,
            Assertion(assertion_id="x", type="llm_judge", spec=spec),
            alert_webhook="http://x",
        )

        asyncio.run(runner.evaluate_trace(_make_trace()))

        assert queries[0]["static_threshold"] == expected


class TestSerializedEvaluation:
    def test_concurrent_evaluations_do_not_interleave_evaluate_and_query(self) -> None:
        events: list[str] = []
        release_first = asyncio.Event()
        client = MagicMock()

        async def evaluate_batch(trace: Trace, assertions: list[Assertion]) -> EvaluateBatchResult:
            events.append(f"evaluate:{trace.trace_id}")
            if trace.trace_id == "first":
                await release_first.wait()
            return EvaluateBatchResult(
                results=[
                    AssertionResult(
                        assertion_id=a.assertion_id,
                        status="hard_fail",
                        score=0.1,
                        explanation="e",
                    )
                    for a in assertions
                ],
                usage=Usage(),
            )

        async def send_request(method: str, params: dict[str, object]) -> dict[str, object]:
            events.append("query")
            return {
                "report": {
                    "assertion_id": params["assertion_id"],
                    "mean": 0.9,
                    "stddev": 0.0,
                    "count": 10,
                    "latest_score": 0.1,
                    "deviation": 8.0,
                    "status": "drift_detected",
                }
            }

        client.evaluate_batch = evaluate_batch
        client.send_request = send_request
        runner, sent = TestDriftAlerts._runner(
            client, TestDriftAlerts._assertion(), alert_webhook="http://x"
        )

        async def scenario() -> None:
            first = asyncio.create_task(runner.evaluate_trace(_make_trace("first")))
            await asyncio.sleep(0.05)
            second = asyncio.create_task(runner.evaluate_trace(_make_trace("second")))
            await asyncio.sleep(0.05)
            release_first.set()
            await asyncio.gather(first, second)

        asyncio.run(scenario())

        assert events == ["evaluate:first", "query", "evaluate:second", "query"]
        assert [a["trace_id"] for a in sent] == ["first", "second"]


class TestDispatchDeadline:
    def test_a_receiver_trickling_bytes_cannot_outlive_the_total_deadline(self) -> None:
        import socket
        import threading
        import time

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        port = listener.getsockname()[1]

        def trickle() -> None:
            conn, _ = listener.accept()
            conn.recv(65536)
            conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 40\r\n\r\n")
            try:
                for _ in range(40):
                    conn.sendall(b"x")
                    time.sleep(0.03)
            except OSError:
                pass
            finally:
                conn.close()

        thread = threading.Thread(target=trickle, daemon=True)
        thread.start()
        dispatcher = AlertDispatcher(webhook_url=f"http://127.0.0.1:{port}/", timeout_s=0.1)

        started = time.monotonic()
        failures = asyncio.run(dispatcher.dispatch({"score": 0.1}))
        elapsed = time.monotonic() - started
        listener.close()

        assert elapsed < 0.4
        assert len(failures) == 1
        assert isinstance(failures[0], AlertDispatchError)
        assert "timed out" in str(failures[0])


class TestDispatchWorkerThread:
    """The I/O itself must end at the deadline, not just the awaiting coroutine."""

    @staticmethod
    def _serve_trickle(head: bytes, body_bytes: int) -> tuple[int, Any]:
        import socket
        import threading
        import time

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)

        def trickle() -> None:
            conn, _ = listener.accept()
            conn.recv(65536)
            conn.sendall(head)
            try:
                for _ in range(body_bytes):
                    conn.sendall(b"x")
                    time.sleep(0.03)
            except OSError:
                pass
            finally:
                conn.close()

        threading.Thread(target=trickle, daemon=True).start()
        return listener.getsockname()[1], listener

    @pytest.mark.parametrize(
        "head",
        [
            b"HTTP/1.1 200 OK\r\nContent-Length: 200\r\n\r\n",
            b"HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n",
            b"HTTP/1.0 200 OK\r\n\r\n",
        ],
        ids=["keep-alive", "connection-close", "http-1.0"],
    )
    def test_post_worker_thread_returns_at_the_deadline(self, head: bytes) -> None:
        import threading
        import time

        port, listener = self._serve_trickle(head, 200)
        timeout_s = 0.05
        dispatcher = AlertDispatcher(webhook_url=f"http://127.0.0.1:{port}/", timeout_s=timeout_s)
        outcome: list[BaseException] = []

        def worker() -> None:
            try:
                dispatcher._post_json(f"http://127.0.0.1:{port}/", {"score": 0.1})
            except AlertDispatchError as exc:
                outcome.append(exc)

        thread = threading.Thread(target=worker)
        started = time.monotonic()
        thread.start()
        thread.join(timeout=timeout_s + 0.1)
        elapsed = time.monotonic() - started
        alive = thread.is_alive()
        thread.join(timeout=5)
        listener.close()

        assert not alive, f"worker still running {elapsed:.3f}s after start"
        assert len(outcome) == 1
        assert "timed out" in str(outcome[0])


class _Receiver:
    """Local HTTP server that records every POSTed JSON body."""

    def __init__(self) -> None:
        import json
        import threading
        from http.server import BaseHTTPRequestHandler, HTTPServer

        self.bodies: list[dict[str, Any]] = []
        bodies = self.bodies

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802
                length = int(self.headers["Content-Length"])
                bodies.append(json.loads(self.rfile.read(length)))
                self.send_response(200)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                return

        self._server = HTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self._server.server_address[1]}/hook"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()
        self._thread.join()


class TestConfiguredAlertTargets:
    """attest.config() alert targets are the runner's defaults; explicit arguments win."""

    @pytest.fixture
    def receivers(self) -> Any:
        from attest.config import reset

        generic, slack, other = _Receiver(), _Receiver(), _Receiver()
        yield generic, slack, other
        reset()
        for receiver in (generic, slack, other):
            receiver.close()

    @staticmethod
    def _runner(**kwargs: Any) -> ContinuousEvalRunner:
        client, _ = TestDriftAlerts._client("pass", "drift_detected")
        return ContinuousEvalRunner(
            client=client,
            assertions=[TestDriftAlerts._assertion(threshold="dynamic")],
            name="cfg",
            sample_rate=1.0,
            **kwargs,
        )

    @pytest.mark.asyncio
    async def test_runner_configured_only_through_attest_config_alerts_both_targets(
        self, receivers: Any
    ) -> None:
        import attest

        generic, slack, _ = receivers
        attest.config(alert_webhook=generic.url, alert_slack_url=slack.url)
        runner = self._runner()

        await runner.evaluate_trace(_make_trace("t-cfg"))

        assert [b["trace_id"] for b in generic.bodies] == ["t-cfg"]
        assert len(slack.bodies) == 1
        assert "t-cfg" in slack.bodies[0]["text"]

    @pytest.mark.asyncio
    async def test_explicit_runner_arguments_override_attest_config(self, receivers: Any) -> None:
        import attest

        generic, slack, other = receivers
        attest.config(alert_webhook=generic.url, alert_slack_url=slack.url)
        runner = self._runner(alert_webhook=other.url)

        await runner.evaluate_trace(_make_trace("t-explicit"))

        assert [b["trace_id"] for b in other.bodies] == ["t-explicit"]
        assert generic.bodies == []
        assert len(slack.bodies) == 1
