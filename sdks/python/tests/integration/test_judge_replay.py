"""L6 from replay: ``passes_judge`` runs on a real engine whose judge endpoint
(``ATTEST_JUDGE_BASE_URL``) is the replay proxy, so no model is called.

The fixtures in ``fixtures/judge/`` were recorded against the scripted judge in
``judge_support``; ``make record-judge-fixtures`` re-records them against a real provider.
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from pathlib import Path

import pytest

from attest import ReplaySummary, egress, load_fixture, start_replay
from attest.result import AgentResult
from tests.integration.conftest import _engine_binary_path
from tests.integration.judge_support import FIXTURE_DIR, SCENARIOS, JudgeScenario, run_judge


@pytest.fixture
def no_provider(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Iterator[None]:
    """No provider key anywhere, the egress guard on and the built engine selected."""
    engine = _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    for key in (
        "OPENAI_API_KEY",
        "ANTHROPIC_API_KEY",
        "GOOGLE_API_KEY",
        "GEMINI_API_KEY",
        "ATTEST_JUDGE_API_KEY",
        "ATTEST_SIMULATION",
    ):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", engine)
    monkeypatch.setenv("ATTEST_ENGINE_NO_DOWNLOAD", "1")
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "cache"))
    egress.install()
    yield
    egress.uninstall()


def _judge_from_replay(
    scenario: JudgeScenario, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> tuple[AgentResult, ReplaySummary]:
    fixture = load_fixture(FIXTURE_DIR / f"{scenario.name}.attest.json")
    model = fixture.exchanges[0]["request"]["model"]
    with start_replay(scenario.name, mode="strict", fixtures_dir=FIXTURE_DIR) as session:
        monkeypatch.setenv("ATTEST_JUDGE_PROVIDER", "openai")
        monkeypatch.setenv("ATTEST_JUDGE_BASE_URL", session.env["OPENAI_BASE_URL"])
        monkeypatch.setenv("ATTEST_JUDGE_API_KEY", "replay-never-sent-upstream")
        monkeypatch.setenv("ATTEST_JUDGE_MODEL", model)
        result = run_judge(scenario, cache_dir=tmp_path / "judge-cache")
    assert session.summary is not None
    return result, session.summary


@pytest.mark.integration
@pytest.mark.parametrize("scenario", SCENARIOS, ids=lambda s: s.name)
def test_judge_replay_serves_l6_verdicts_from_the_fixture_with_zero_live_calls(
    scenario: JudgeScenario,
    no_provider: None,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    result, summary = _judge_from_replay(scenario, tmp_path, monkeypatch)

    (row,) = result.assertion_results
    assert row.layer == 6
    assert row.status == ("pass" if scenario.passes else "hard_fail")
    assert row.score == pytest.approx(scenario.scripted_score)
    assert (summary.replayed, summary.live, summary.misses) == (1, 0, 0)


@pytest.mark.integration
def test_judge_replay_with_a_changed_rubric_is_a_replay_miss(
    no_provider: None, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    from attest import ReplayMissError, expect
    from attest.plugin import AttestEngineFixture
    from tests.integration.judge_support import SCRIPTED_MODEL, _judged_agent

    scenario = SCENARIOS[0]
    with pytest.raises(ReplayMissError, match=r"replay miss: messages changed"):
        with start_replay(scenario.name, mode="strict", fixtures_dir=FIXTURE_DIR) as session:
            monkeypatch.setenv("ATTEST_JUDGE_PROVIDER", "openai")
            monkeypatch.setenv("ATTEST_JUDGE_BASE_URL", session.env["OPENAI_BASE_URL"])
            monkeypatch.setenv("ATTEST_JUDGE_API_KEY", "replay-never-sent-upstream")
            monkeypatch.setenv("ATTEST_JUDGE_MODEL", SCRIPTED_MODEL)
            monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "other-cache"))
            engine = AttestEngineFixture(engine_path=None, log_level="warn", strict=False)
            engine.start()
            try:
                engine.inspect(
                    expect(_judged_agent(output=scenario.output)).passes_judge("Is it polite?")
                )
            finally:
                engine.stop()
