"""The drafted replay test: what it runs, and how replay_run drives the engine."""

from __future__ import annotations

import ast
import json
import stat
import sys
from pathlib import Path

import pytest

import attest.engine_manager
from attest.capture import CaptureError, Rerun, load_fixture, render_pytest, replay_run
from attest.cli.admin import _world_path

_VALID = Path(__file__).resolve().parents[3] / "protocol-tests" / "fixture-v1" / "valid"
_COMMAND_FIXTURE = _VALID / "001-command-capture-expect-fail.attest.json"
_OTLP_FIXTURE = _VALID / "002-otlp-file-trace-only.attest.json"

# A stand-in engine: records how it was called and writes a trace file, as
# `attest-engine replay --trace-out` does. Exit code and stderr come from the environment.
_FAKE_ENGINE = f"""#!{sys.executable}
import json, os, sys
argv = sys.argv[1:]
out = argv[argv.index("--trace-out") + 1]
with open(os.environ["FAKE_ENGINE_LOG"], "w") as f:
    json.dump({{"argv": argv, "cwd": os.getcwd(), "env": dict(os.environ)}}, f)
if os.environ.get("FAKE_ENGINE_FAIL"):
    sys.stderr.write(os.environ["FAKE_ENGINE_FAIL"])
    sys.exit(1)
with open(out, "w") as f:
    f.write(os.environ["FAKE_ENGINE_TRACE"])
"""


@pytest.fixture
def fake_engine(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    script = tmp_path / "fake-engine"
    script.write_text(_FAKE_ENGINE)
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    log = tmp_path / "engine-call.json"
    monkeypatch.setattr(attest.engine_manager, "resolve_engine", lambda explicit=None: str(script))
    monkeypatch.setenv("FAKE_ENGINE_LOG", str(log))
    monkeypatch.setenv(
        "FAKE_ENGINE_TRACE", json.dumps(json.loads(_COMMAND_FIXTURE.read_text())["trace"])
    )
    return log


def _draft(tmp_path: Path, rerun: Rerun | None, fixture_file: Path = _COMMAND_FIXTURE) -> str:
    fixture = load_fixture(fixture_file)
    return render_pytest(
        fixture, tmp_path / "fixtures" / "inc.attest.json", tmp_path / "test_inc.py", rerun
    )


def test_render_pytest_command_fixture_reruns_agent_and_evaluates_new_trace(
    tmp_path: Path,
) -> None:
    source = _draft(tmp_path, Rerun(tmp_path))

    ast.parse(source)
    assert "trace = replay_run(FIXTURE, cwd=RUN_DIR, world=WORLD)" in source
    assert "attest.evaluate(contracts(trace))" in source
    assert "return (\n        expect(trace)" in source


def test_render_pytest_command_fixture_does_not_evaluate_the_recording(tmp_path: Path) -> None:
    source = _draft(tmp_path, Rerun(tmp_path))

    assert "FIXTURE.trace" not in source
    assert "load_fixture" not in source
    assert "attest.inspect" not in source
    assert "EXPECTED" not in source


def test_render_pytest_command_fixture_says_consistent_with_the_recording(tmp_path: Path) -> None:
    source = _draft(tmp_path, Rerun(tmp_path))

    assert "consistent with the recording" in source
    assert "strict replay with egress blocked" in source
    assert "verified" not in source.lower()


def test_render_pytest_command_fixture_states_the_identity_limit(tmp_path: Path) -> None:
    source = _draft(tmp_path, Rerun(tmp_path))

    assert "relative identity" in source
    assert "absolute" in source


def test_render_pytest_rerun_paths_are_relative_to_the_test(tmp_path: Path) -> None:
    rerun = Rerun(tmp_path / "proj", tmp_path / "proj" / "world" / "w.yaml")

    source = _draft(tmp_path, rerun)

    assert 'RUN_DIR = Path(__file__).parent / "proj"' in source
    assert 'WORLD = Path(__file__).parent / "proj/world/w.yaml"' in source


def test_render_pytest_rerun_without_world_leaves_world_none(tmp_path: Path) -> None:
    source = _draft(tmp_path, Rerun(tmp_path))

    assert "RUN_DIR = Path(__file__).parent\n" in source
    assert "WORLD = None\n" in source


def test_render_pytest_otlp_fixture_with_rerun_keeps_the_trace_only_draft(tmp_path: Path) -> None:
    source = _draft(tmp_path, Rerun(tmp_path), _OTLP_FIXTURE)

    assert "replay_run" not in source
    assert "attest.inspect(contracts())" in source


def test_render_pytest_command_fixture_without_rerun_keeps_the_trace_only_draft(
    tmp_path: Path,
) -> None:
    source = _draft(tmp_path, None)

    assert "replay_run" not in source
    assert "load_fixture(" in source


def test_replay_run_runs_source_command_under_strict_no_live_replay(
    fake_engine: Path, tmp_path: Path
) -> None:
    replay_run(_COMMAND_FIXTURE, cwd=tmp_path)

    call = json.loads(fake_engine.read_text())
    argv = call["argv"]
    assert argv[0] == "replay"
    assert argv[argv.index("--mode") + 1] == "strict"
    assert "--no-live" in argv
    assert argv[argv.index("--fixture") + 1] == str(_COMMAND_FIXTURE.resolve())
    assert argv[argv.index("--") + 1 :] == ["agent", "--key", "[redacted]"]
    assert Path(call["cwd"]).resolve() == tmp_path.resolve()


def test_replay_run_blocks_egress_and_strips_provider_keys(
    fake_engine: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-ant-real")
    monkeypatch.setenv("OPENAI_API_KEY", "sk-real")

    replay_run(_COMMAND_FIXTURE)

    env = json.loads(fake_engine.read_text())["env"]
    assert env["ATTEST_EGRESS"] == "block"
    assert "ANTHROPIC_API_KEY" not in env
    assert env["OPENAI_API_KEY"] == "replay-needs-no-key"


def test_replay_run_returns_the_new_runs_trace(fake_engine: Path) -> None:
    trace = replay_run(_COMMAND_FIXTURE)

    assert trace.to_dict() == load_fixture(_COMMAND_FIXTURE).trace.to_dict()


def test_replay_run_passes_the_world_file(fake_engine: Path, tmp_path: Path) -> None:
    doc = json.loads(_COMMAND_FIXTURE.read_text())
    doc["world"] = {
        "definition_sha256": "0" * 64,
        "faults_fired": [],
        "faults_fired_count": 0,
        "state": {},
        "world_version": 1,
    }
    fixture = tmp_path / "w.attest.json"
    fixture.write_text(json.dumps(doc))
    world = tmp_path / "w.world.yaml"
    world.write_text("x")

    replay_run(fixture, world=world)

    argv = json.loads(fake_engine.read_text())["argv"]
    assert argv[argv.index("--world") + 1] == str(world.resolve())


def test_replay_run_world_fixture_without_world_file_raises(tmp_path: Path) -> None:
    doc = json.loads(_COMMAND_FIXTURE.read_text())
    doc["world"] = {
        "definition_sha256": "0" * 64,
        "faults_fired": [],
        "faults_fired_count": 0,
        "state": {},
        "world_version": 1,
    }
    fixture = tmp_path / "w.attest.json"
    fixture.write_text(json.dumps(doc))

    with pytest.raises(CaptureError, match="captured with a world"):
        replay_run(fixture)


def test_replay_run_engine_failure_raises_with_the_engine_message(
    fake_engine: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("FAKE_ENGINE_FAIL", "replay: 1 request matched no recording")

    with pytest.raises(CaptureError, match="matched no recording"):
        replay_run(_COMMAND_FIXTURE)


def test_replay_run_trace_only_fixture_raises(fake_engine: Path) -> None:
    with pytest.raises(CaptureError, match="not a command capture"):
        replay_run(_OTLP_FIXTURE)


@pytest.mark.parametrize(
    ("args", "expected"),
    [
        (["--name", "x", "--world", "w.yaml", "--", "cmd"], "w.yaml"),
        (["--name", "x", "--world=w.yaml", "--", "cmd"], "w.yaml"),
        (["--name", "x", "-world", "w.yaml"], "w.yaml"),
        (["--name", "x", "--", "cmd", "--world", "ignored.yaml"], None),
        (["--name", "x", "--", "cmd"], None),
    ],
)
def test_world_path_reads_the_capture_world_flag(args: list[str], expected: str | None) -> None:
    got = _world_path(args)

    assert got == (Path(expected).resolve() if expected else None)
