"""The running test's identity reaches ``evaluate_batch`` as its ``context`` param."""

from __future__ import annotations

from collections.abc import Generator
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from attest import plugin
from attest._proto.types import BatchContext, Trace, Usage
from attest.client import AttestClient
from attest.trials import TestRun, begin_run, end_run


@pytest.mark.asyncio
async def test_evaluate_batch_with_context_sends_set_fields_only() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(return_value={"results": [], "usage": Usage().to_dict()})  # type: ignore[method-assign]

    await client.evaluate_batch(
        Trace(trace_id="t", output={}),
        [],
        context=BatchContext(test_id="tests/test_a.py::test_b", file="tests/test_a.py"),
    )

    params: dict[str, Any] = client.send_request.call_args[0][1]
    assert params["context"] == {"test_id": "tests/test_a.py::test_b", "file": "tests/test_a.py"}


@pytest.mark.asyncio
async def test_evaluate_batch_without_context_omits_the_param() -> None:
    client = AttestClient(MagicMock())
    client.send_request = AsyncMock(  # type: ignore[method-assign]
        return_value={"results": [], "usage": Usage().to_dict()}
    )

    await client.evaluate_batch(Trace(trace_id="t", output={}), [])

    assert "context" not in client.send_request.call_args[0][1]


@pytest.fixture
def run() -> Generator[TestRun, None, None]:
    test_run = TestRun(test_id="tests/test_a.py::test_b", file="tests/test_a.py", line=7)
    begin_run(test_run)
    yield test_run
    end_run()


def test_plugin_batch_context_follows_running_test(run: TestRun) -> None:
    assert plugin._batch_context() == BatchContext(
        test_id="tests/test_a.py::test_b", file="tests/test_a.py", line=7
    )


def test_plugin_batch_context_outside_a_test_is_none() -> None:
    assert plugin._batch_context() is None


def test_runtest_call_hook_sets_file_and_one_based_line_of_the_test(tmp_path: Path) -> None:
    item = MagicMock()
    item.nodeid = "tests/test_a.py::test_b"
    item.fixturenames = ["attest"]
    item.location = ("tests/test_a.py", 6, "test_b")  # pytest's line is 0-based
    item.get_closest_marker.return_value = None
    hook = plugin.pytest_runtest_call(item)
    next(hook)
    try:
        assert plugin._batch_context() == BatchContext(
            test_id="tests/test_a.py::test_b", file="tests/test_a.py", line=7
        )
    finally:
        outcome = MagicMock()
        outcome.get_result.return_value = None
        with pytest.raises(StopIteration):
            hook.send(outcome)


def test_runtest_call_hook_omits_line_when_pytest_has_none() -> None:
    item = MagicMock()
    item.nodeid = "tests/test_a.py::test_b"
    item.fixturenames = ["attest"]
    item.location = ("tests/test_a.py", None, "test_b")
    item.get_closest_marker.return_value = None
    hook = plugin.pytest_runtest_call(item)
    next(hook)
    try:
        context = plugin._batch_context()
        assert context == BatchContext(test_id="tests/test_a.py::test_b", file="tests/test_a.py")
        assert context is not None and "line" not in context.to_dict()
    finally:
        outcome = MagicMock()
        outcome.get_result.return_value = None
        with pytest.raises(StopIteration):
            hook.send(outcome)
