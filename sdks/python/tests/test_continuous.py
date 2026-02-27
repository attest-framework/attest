"""Tests for the continuous evaluation runner."""

from __future__ import annotations

import asyncio
import urllib.error
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from attest._proto.types import Assertion, EvaluateBatchResult, AssertionResult, Trace
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
            {"drift_type": "cosine", "score": 0.42, "trace_id": "t-99"}
        )
        assert "drift" in msg
        assert "cosine" in msg
        assert "0.42" in msg
        assert "t-99" in msg


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
            )
        assert runner._queue.maxsize == 200

    def test_submit_drops_trace_when_queue_full(self) -> None:
        """submit() drops excess traces and logs a warning instead of blocking."""
        client = MagicMock()
        client.evaluate_batch = AsyncMock(return_value=_make_batch_result())
        runner = ContinuousEvalRunner(
            client=client,
            assertions=[_make_assertion()],
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
