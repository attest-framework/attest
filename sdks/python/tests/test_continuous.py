"""Tests for the continuous evaluation runner."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest._proto.types import Assertion, AssertionResult, EvaluateBatchResult, Trace, Usage
from attest.assertion_id import compute_assertion_id
from attest.continuous import AlertDispatcher, ContinuousEvalRunner, Sampler

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
        alert = {"drift_type": "cosine", "score": 0.3, "trace_id": "t-1"}

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
        alert = {"drift_type": "cosine", "score": 0.3}

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
        client, queries = self._client("hard_fail", "drift_detected")
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
