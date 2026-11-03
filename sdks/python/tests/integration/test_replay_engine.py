"""Replay against the built engine: capture a scripted agent, then replay it with the
upstream gone, no provider keys and the egress guard on."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import textwrap
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest import (
    ReplayMissError,
    ReplayNotEvaluatedError,
    egress,
    record,
    start_replay,
)
from attest.replay import live_line
from tests.integration.test_capture_engine import (
    _PLANTED_KEY,
    _scripted_agent,
    engine_env,  # noqa: F401  (fixture)
)

_SCHEMA: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[4] / "proto" / "attest" / "v1" / "report-v2.schema.json"
    ).read_text()
)
_DEAD_UPSTREAM = "http://127.0.0.1:1/v1"
_MISS = re.compile(
    r"replay miss: system prompt changed \(hash ([0-9a-f]{7})→([0-9a-f]{7})\); "
    r"re-record with `attest capture --refresh inc-9`"
)
_V1 = "You refund an order once."
_V2 = "You refund an order twice."


@pytest.fixture
def captured(engine_env: str, tmp_path: Path) -> Path:  # noqa: F811
    """A fixture directory holding ``inc-9``, captured with system prompt ``_V1``."""
    out_dir = tmp_path / "tests" / "attest" / "fixtures"
    with record("inc-9", out_dir=out_dir, expect_fail=True, emit_test=False):
        _scripted_agent(refunds=2, system=_V1)
    return out_dir


@pytest.fixture
def no_upstream(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """The model upstream is a closed port, no provider key is set and the egress guard is on."""
    monkeypatch.setenv("OPENAI_BASE_URL", _DEAD_UPSTREAM)
    for key in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ATTEST_JUDGE_API_KEY"):
        monkeypatch.delenv(key, raising=False)
    egress.install()
    yield
    egress.uninstall()


def _run_agent(system: str) -> None:
    """The scripted agent. Its client wants a key; the engine never sees it (it started
    before the key was set), so a miss still cannot go live."""
    os.environ["OPENAI_API_KEY"] = _PLANTED_KEY
    try:
        _scripted_agent(refunds=2, system=system)
    finally:
        del os.environ["OPENAI_API_KEY"]


@pytest.mark.integration
def test_replay_recorded_run_serves_every_call_with_zero_live_calls(
    captured: Path, no_upstream: None
) -> None:
    before = os.environ["OPENAI_BASE_URL"]

    with start_replay("inc-9", mode="strict", fixtures_dir=captured) as session:
        assert os.environ["OPENAI_BASE_URL"] == session.env["OPENAI_BASE_URL"] != before
        _run_agent(_V1)

    assert os.environ["OPENAI_BASE_URL"] == before
    summary = session.summary
    assert summary is not None
    assert (summary.replayed, summary.live, summary.misses) == (1, 0, 0)
    assert live_line(summary.live, summary.live_cost_usd, summary.unpriced_live_calls) == (
        "0 live model calls · $0.00"
    )


@pytest.mark.integration
def test_replay_system_prompt_edited_strict_raises_the_miss_message(
    captured: Path, no_upstream: None
) -> None:
    with pytest.raises(ReplayMissError) as info:
        with start_replay("inc-9", mode="strict", fixtures_dir=captured):
            _run_agent(_V2)

    match = _MISS.fullmatch(str(info.value))
    assert match is not None, str(info.value)
    assert match.group(1) != match.group(2)


@pytest.mark.integration
def test_replay_miss_replaces_the_error_the_agent_raised(captured: Path, no_upstream: None) -> None:
    with pytest.raises(ReplayMissError):
        with start_replay("inc-9", mode="strict", fixtures_dir=captured):
            _run_agent(_V2)  # the 422 raises HTTPError inside the block


@pytest.mark.integration
def test_replay_auto_without_key_is_not_evaluated_and_makes_no_live_call(
    captured: Path, no_upstream: None
) -> None:
    with pytest.raises(ReplayNotEvaluatedError, match="system prompt changed"):
        with start_replay("inc-9", mode="auto", fixtures_dir=captured) as session:
            _run_agent(_V2)

    assert session.summary is not None
    assert (session.summary.live, session.summary.misses) == (0, 1)


@pytest.mark.integration
def test_replay_off_leaves_environment_and_starts_nothing(
    captured: Path, no_upstream: None
) -> None:
    before = dict(os.environ)

    with start_replay("inc-9", mode="off", fixtures_dir=captured) as session:
        assert dict(os.environ) == before

    assert session.summary is None


@pytest.mark.integration
def test_replay_missing_fixture_raises_replay_error(engine_env: str, tmp_path: Path) -> None:  # noqa: F811
    from attest import ReplayError

    with pytest.raises(ReplayError, match="absent"):
        start_replay("absent", mode="strict", fixtures_dir=tmp_path)


_AGENT_TEST = textwrap.dedent(
    """
    import json, os, urllib.request

    def call_model(system):
        key = os.environ.get("OPENAI_API_KEY", "unused")
        body = {"model": "gpt-test", "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": "refund O-9 for sam.customer@example.net"}]}
        req = urllib.request.Request(
            os.environ["OPENAI_BASE_URL"] + "/chat/completions",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + key})
        return urllib.request.urlopen(req).read()

    def test_inc_9_replay(attest_replay):
        with attest_replay("inc-9"):
            call_model(os.environ["AGENT_SYSTEM"])

    def test_inc_9_replay_without_with(attest_replay):
        attest_replay("inc-9")
        call_model(os.environ["AGENT_SYSTEM"])
    """
)


_AGENT_HELPERS = _AGENT_TEST.split("def test_inc_9_replay(")[0]

_XFAIL_TEST = _AGENT_HELPERS + textwrap.dedent(
    """
    import pytest

    @pytest.mark.xfail(strict=True)
    def test_current_agent_under_xfail(attest_replay):
        with attest_replay("inc-9"):
            call_model(os.environ["AGENT_SYSTEM"])
            raise AssertionError("the agent's own failure: INC-1042 reproduces")

    @pytest.mark.xfail(strict=True)
    def test_current_agent_under_xfail_without_with(attest_replay):
        attest_replay("inc-9")
        call_model(os.environ["AGENT_SYSTEM"])
        raise AssertionError("the agent's own failure: INC-1042 reproduces")
    """
)

# The agent runs under replay inside a fixture, so a miss surfaces during setup.
_SETUP_XFAIL_TEST = _AGENT_HELPERS + textwrap.dedent(
    """
    import pytest

    @pytest.fixture
    def incident_with(attest_replay):
        with attest_replay("inc-9"):
            call_model(os.environ["AGENT_SYSTEM"])
        raise AssertionError("the agent's own failure, raised in setup")

    @pytest.fixture
    def incident_without_with(attest_replay):
        attest_replay("inc-9")
        call_model(os.environ["AGENT_SYSTEM"])
        raise AssertionError("the agent's own failure, raised in setup")

    @pytest.mark.xfail(strict=True)
    def test_incident_from_fixture_with(incident_with):
        pass

    @pytest.mark.xfail(strict=True)
    def test_incident_from_fixture_without_with(incident_without_with):
        pass
    """
)

_TRIALS_TEST = _AGENT_HELPERS + textwrap.dedent(
    """
    def test_trials(attest, attest_replay):
        attest_replay("inc-9")
        call_model(os.environ["AGENT_SYSTEM"])
    """
)


def _run_pytest(
    captured: Path,
    tmp_path: Path,
    system: str,
    *args: str,
    source: str = _AGENT_TEST,
    **env: str,
) -> tuple[subprocess.CompletedProcess[str], dict[str, Any]]:
    project = captured.parents[2]
    (project / "test_agent.py").write_text(source)
    report = tmp_path / "report.json"
    child_env = {
        k: v
        for k, v in os.environ.items()
        if k not in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ATTEST_JUDGE_API_KEY", "CI")
    }
    child_env.update(AGENT_SYSTEM=system, OPENAI_BASE_URL=_DEAD_UPSTREAM, **env)
    run = subprocess.run(
        [
            sys.executable,
            "-m",
            "pytest",
            "-q",
            "-p",
            "no:cacheprovider",
            "--attest-egress=block",
            f"--attest-report={report}",
            *args,
            "test_agent.py",
        ],  # fmt: skip
        capture_output=True,
        text=True,
        check=False,
        cwd=project,
        env=child_env,
    )
    return run, json.loads(report.read_text())


@pytest.mark.integration
def test_pytest_attest_replay_strict_passes_with_zero_live_calls_and_reports_provenance(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(captured, tmp_path, _V1, "--attest-replay=strict")

    assert run.returncode == 0, run.stdout + run.stderr
    assert "2 passed" in run.stdout
    assert "0 live model calls · $0.00" in run.stdout
    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert report["summary"]["replay"] == {
        "replayed": 2,
        "live": 0,
        "misses": 0,
        "live_cost_usd": 0.0,
        "unpriced_live_calls": 0,
    }
    assert [t["replay"]["mode"] for t in report["tests"]] == ["strict", "strict"]


@pytest.mark.integration
def test_pytest_attest_replay_strict_system_prompt_edit_fails_with_the_miss_message(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(captured, tmp_path, _V2, "--attest-replay=strict")

    assert run.returncode == 1, run.stdout + run.stderr
    assert "2 failed" in run.stdout
    assert _MISS.search(run.stdout) is not None, run.stdout
    assert "0 live model calls · $0.00" in run.stdout
    assert [t["outcome"] for t in report["tests"]] == ["failed", "failed"]
    assert all(_MISS.fullmatch(t["replay"]["miss"]) for t in report["tests"])


@pytest.mark.integration
def test_pytest_attest_replay_auto_without_key_skips_with_replay_miss(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(captured, tmp_path, _V2, "--attest-replay=auto", "-rs")

    assert run.returncode == 0, run.stdout + run.stderr
    assert "2 skipped" in run.stdout
    assert "not evaluated: replay_miss" in run.stdout
    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert report["summary"]["not_evaluated"] == 2
    assert [t["not_evaluated_reasons"] for t in report["tests"]] == [["replay_miss"]] * 2
    assert report["summary"]["replay"]["live"] == 0


@pytest.mark.integration
def test_pytest_attest_replay_auto_under_attest_strict_fails(
    captured: Path, tmp_path: Path
) -> None:
    run, _ = _run_pytest(captured, tmp_path, _V2, "--attest-replay=auto", "--attest-strict")

    assert run.returncode == 1, run.stdout + run.stderr
    assert "2 failed" in run.stdout


@pytest.mark.integration
def test_pytest_attest_replay_default_mode_is_strict_when_ci_is_set(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(captured, tmp_path, _V2, CI="true")

    assert run.returncode == 1, run.stdout + run.stderr
    assert {t["replay"]["mode"] for t in report["tests"]} == {"strict"}


@pytest.mark.integration
def test_pytest_attest_replay_off_runs_the_agent_live_and_not_replayed(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(captured, tmp_path, _V1, "--attest-replay=off")

    assert run.returncode == 1, run.stdout + run.stderr  # the dead upstream refuses the call
    assert "replay" not in report["summary"]


@pytest.mark.integration
def test_pytest_strict_miss_under_xfail_strict_is_failed_with_the_miss_message(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(
        captured, tmp_path, _V2, "--attest-replay=strict", "-rxX", source=_XFAIL_TEST
    )

    assert run.returncode == 1, run.stdout + run.stderr
    assert "2 failed" in run.stdout and "xfailed" not in run.stdout
    assert _MISS.search(run.stdout) is not None, run.stdout
    assert [t["outcome"] for t in report["tests"]] == ["failed", "failed"]


@pytest.mark.integration
def test_pytest_no_miss_under_xfail_strict_is_xfail_as_usual(
    captured: Path, tmp_path: Path
) -> None:
    run, _ = _run_pytest(captured, tmp_path, _V1, "--attest-replay=strict", source=_XFAIL_TEST)

    assert run.returncode == 0, run.stdout + run.stderr
    assert "2 xfailed" in run.stdout
    assert "0 live model calls · $0.00" in run.stdout


@pytest.mark.integration
def test_pytest_attest_trials_with_unscoped_replay_ends_each_trials_session(
    captured: Path, tmp_path: Path
) -> None:
    run, _ = _run_pytest(
        captured, tmp_path, _V1, "--attest-replay=strict", "--attest-trials=2", source=_TRIALS_TEST
    )

    assert "already running" not in run.stdout + run.stderr
    assert run.returncode == 0, run.stdout + run.stderr
    assert "attest replay: 2 replayed" in run.stdout


@pytest.mark.integration
def test_pytest_auto_with_matching_key_but_egress_blocked_is_not_evaluated_and_not_live(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(
        captured,
        tmp_path,
        _V2,
        "--attest-replay=auto",
        "-rs",
        OPENAI_API_KEY=_PLANTED_KEY,
    )

    assert run.returncode == 0, run.stdout + run.stderr
    assert "2 skipped" in run.stdout and "0 live model calls" in run.stdout
    assert report["summary"]["replay"]["live"] == 0
    assert [t["not_evaluated_reasons"] for t in report["tests"]] == [["replay_miss"]] * 2


@pytest.mark.integration
@pytest.mark.parametrize(
    "system, mode, outcome",
    [(_V2, "strict", "errors"), (_V1, "strict", "xfailed"), (_V2, "auto", "skipped")],
)
def test_pytest_replay_in_fixture_setup_under_xfail_reports_a_miss(
    captured: Path, tmp_path: Path, system: str, mode: str, outcome: str
) -> None:
    run, report = _run_pytest(
        captured, tmp_path, system, f"--attest-replay={mode}", "-rsxX", source=_SETUP_XFAIL_TEST
    )

    # A setup failure is an error, not a failed test; a miss is never an expected failure.
    assert f"2 {outcome}" in run.stdout, run.stdout + run.stderr
    if system == _V2:
        assert "xfailed" not in run.stdout
        assert _MISS.search(run.stdout) is not None, run.stdout
        assert [t["replay"]["misses"] for t in report["tests"]] == [1, 1]
        assert report["summary"]["replay"]["misses"] == 2
    if mode == "auto":
        assert "not evaluated: replay_miss" in run.stdout
        assert [t["not_evaluated_reasons"] for t in report["tests"]] == [["replay_miss"]] * 2


@pytest.mark.integration
def test_pytest_egress_allow_flag_overrides_attest_egress_env_for_live_forwarding(
    captured: Path, tmp_path: Path
) -> None:
    run, report = _run_pytest(
        captured,
        tmp_path,
        _V2,
        "--attest-replay=auto",
        "--attest-egress=allow",
        OPENAI_API_KEY=_PLANTED_KEY,
        ATTEST_EGRESS="block",
    )

    # Allowed: the auto misses carry the engine's key, so they are forwarded (to a dead
    # upstream here) instead of being reported as not evaluated.
    assert report["summary"]["replay"]["live"] == 2, run.stdout + run.stderr
    assert all("replay_miss" not in t.get("not_evaluated_reasons", []) for t in report["tests"])
