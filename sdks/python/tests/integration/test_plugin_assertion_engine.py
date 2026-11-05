"""``.plugin()`` against a real engine: the SDK submits the plugin's result before the batch
is evaluated, so the plugin row follows it and a judge assertion in the same batch still
evaluates (against a local scripted judge; no model is called)."""

from __future__ import annotations

import os
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest

from attest import agent, expect
from attest._proto.types import STATUS_HARD_FAIL, STATUS_NOT_EVALUATED, STATUS_PASS, Trace
from attest.plugin import AttestEngineFixture
from attest.plugins import PluginResult
from attest.result import AgentResult
from tests.integration.conftest import _engine_binary_path
from tests.integration.judge_support import (
    RUBRIC,
    SCENARIOS,
    SCRIPTED_MODEL,
    serve_scripted_judge,
)

_JUDGED = SCENARIOS[0]


@agent("refund-agent")
def _refund_agent(builder: Any, output: str) -> dict[str, str]:
    builder.set_metadata(cost_usd=0.0, total_tokens=0, latency_ms=1)
    return {"message": output}


class _RecordingPlugin:
    plugin_type = "assertion"

    def __init__(self, name: str, result: PluginResult) -> None:
        self.name = name
        self._result = result
        self.specs: list[dict[str, Any]] = []

    def execute(self, trace: Trace, spec: dict[str, Any]) -> PluginResult:
        self.specs.append(spec)
        return self._result


@pytest.fixture
def judge_engine(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Iterator[AttestEngineFixture]:
    engine_path = _engine_binary_path()
    if not os.path.exists(engine_path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    server = serve_scripted_judge()
    monkeypatch.setenv("ATTEST_JUDGE_PROVIDER", "openai")
    monkeypatch.setenv("ATTEST_JUDGE_BASE_URL", f"http://127.0.0.1:{server.server_address[1]}/v1")
    monkeypatch.setenv("ATTEST_JUDGE_API_KEY", "scripted-unused")
    monkeypatch.setenv("ATTEST_JUDGE_MODEL", SCRIPTED_MODEL)
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.delenv("ATTEST_SIMULATION", raising=False)
    fixture = AttestEngineFixture(engine_path=engine_path, log_level="warn")
    fixture.start()
    try:
        yield fixture
    finally:
        fixture.stop()
        server.shutdown()


def _chain(plugin_id: str) -> Any:
    result: AgentResult = _refund_agent(output=_JUDGED.output)
    return expect(result).passes_judge(RUBRIC).plugin(plugin_id, {"limit": 3})


@pytest.mark.integration
@pytest.mark.parametrize(
    ("submitted", "expected_status"),
    [
        (PluginResult(status="pass", score=1.0, explanation="plugin says ok"), STATUS_PASS),
        (
            PluginResult(status="hard_fail", score=0.0, explanation="plugin says no"),
            STATUS_HARD_FAIL,
        ),
    ],
    ids=["plugin-pass", "plugin-hard-fail"],
)
def test_plugin_row_follows_its_result_while_the_judge_still_evaluates(
    judge_engine: AttestEngineFixture, submitted: PluginResult, expected_status: str
) -> None:
    plugin = _RecordingPlugin("limit-check", submitted)
    judge_engine.plugin_registry.register("limit-check", plugin)

    result = judge_engine.inspect(_chain("limit-check"))

    by_type = {r.type: r for r in result.assertion_results}
    assert by_type["plugin"].status == expected_status
    assert by_type["plugin"].explanation == submitted.explanation
    assert by_type["llm_judge"].status == STATUS_PASS
    assert by_type["llm_judge"].reason is None
    assert plugin.specs == [{"plugin_id": "limit-check", "soft": False, "config": {"limit": 3}}]


@pytest.mark.integration
def test_plugin_without_a_registered_implementation_is_not_evaluated_not_a_hard_fail(
    judge_engine: AttestEngineFixture,
) -> None:
    result = judge_engine.inspect(_chain("nobody-registered-this"))

    by_type = {r.type: r for r in result.assertion_results}
    assert by_type["plugin"].status == STATUS_NOT_EVALUATED
    assert by_type["plugin"].reason == "missing_evidence"
    assert by_type["llm_judge"].status == STATUS_PASS
