"""Capture: turn a run into a verified fixture, and fixtures into tests.

``attest capture --name <name> -- <command>`` (the engine) records a run's trace and
model exchanges into ``tests/attest/fixtures/<name>.attest.json``. This module is the
Python side:

- :func:`record` captures the code inside a ``with`` block of this process.
- :func:`load_fixture` reads a fixture (``proto/attest/v1/fixture-v1.schema.json``).
- :func:`emit_pytest` writes the fixture's drafted contracts as a pytest file, using the
  ``expect()`` DSL. ``attest capture`` and :func:`record` call it. For a fixture captured
  from a command, the test re-runs that command under strict replay (:func:`replay_run`)
  and evaluates the contracts on the new run's trace.
"""

from __future__ import annotations

import json
import keyword
import os
import re
import subprocess
import sys
import tempfile
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from attest._proto.types import Trace
from attest.exceptions import AttestError

FIXTURE_VERSION = 1
DEFAULT_FIXTURE_DIR = Path("tests") / "attest" / "fixtures"


class CaptureError(AttestError):
    """A capture did not produce a fixture, or produced one that is not verified."""


@dataclass(frozen=True)
class Contract:
    """A drafted contract: an engine assertion spec plus its id and description."""

    id: str
    kind: str
    description: str
    type: str
    spec: dict[str, Any]


@dataclass(frozen=True)
class ContractResult:
    """A contract's status on the captured trace."""

    contract: str
    status: str
    explanation: str


@dataclass(frozen=True)
class Fixture:
    """A capture fixture (fixture-v1)."""

    name: str
    source: dict[str, Any]
    trace: Trace
    exchanges: list[dict[str, Any]]
    contracts: list[Contract]
    verified: bool
    expect_fail: bool
    results: list[ContractResult]
    expected_failures: list[str]
    world: dict[str, Any] | None
    path: Path | None = None

    @property
    def replay_capable(self) -> bool:
        """True when the fixture recorded model exchanges through the capture proxy."""
        return bool(self.source["replay_capable"])


def load_fixture(path: str | os.PathLike[str]) -> Fixture:
    """Read a capture fixture written by ``attest capture`` or :func:`record`.

    Raises:
        CaptureError: The file is not a fixture-v1 document.
    """
    p = Path(path)
    doc = json.loads(p.read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("fixture_version") != FIXTURE_VERSION:
        raise CaptureError(f"{p}: not a fixture_version {FIXTURE_VERSION} capture fixture")
    _validate(p, doc)
    verification = doc["verification"]
    return Fixture(
        name=doc["name"],
        source=doc["source"],
        trace=Trace.from_dict(doc["trace"]),
        exchanges=doc["exchanges"],
        contracts=[
            Contract(c["id"], c["kind"], c["description"], c["type"], c["spec"])
            for c in doc["contracts"]
        ],
        verified=verification["verified"],
        expect_fail=verification["expect_fail"],
        results=[
            ContractResult(r["contract"], r["status"], r["explanation"])
            for r in verification["results"]
        ],
        expected_failures=list(verification["expected_failures"]),
        world=doc["world"],
        path=p,
    )


#: Fixture names, as the engine accepts them (they become file names).
NAME_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")

_REQUIRED: dict[str, type] = {
    "name": str,
    "source": dict,
    "trace": dict,
    "exchanges": list,
    "contracts": list,
    "verification": dict,
}
_CONTRACT_FIELDS: dict[str, type] = {
    "id": str,
    "kind": str,
    "description": str,
    "type": str,
    "spec": dict,
}
_RESULT_FIELDS: dict[str, type] = {"contract": str, "status": str, "explanation": str}
_VERIFICATION_FIELDS: dict[str, type] = {
    "verified": bool,
    "expect_fail": bool,
    "results": list,
    "expected_failures": list,
}


def _validate(p: Path, doc: dict[str, Any]) -> None:
    """Check the fixture-v1 fields this module reads before using any of them."""

    def need(where: str, obj: Any, fields: dict[str, type]) -> None:
        if not isinstance(obj, dict):
            raise CaptureError(f"{p}: {where} is not an object")
        for key, typ in fields.items():
            if not isinstance(obj.get(key), typ):
                raise CaptureError(f"{p}: {where}.{key} is missing or not a {typ.__name__}")

    need("fixture", doc, _REQUIRED)
    if "world" not in doc:
        raise CaptureError(f"{p}: fixture.world is missing")
    if not NAME_PATTERN.fullmatch(doc["name"]):
        raise CaptureError(f"{p}: fixture name {doc['name']!r} is not a valid fixture name")
    need("fixture.verification", doc["verification"], _VERIFICATION_FIELDS)
    for i, c in enumerate(doc["contracts"]):
        need(f"fixture.contracts[{i}]", c, _CONTRACT_FIELDS)
    for i, r in enumerate(doc["verification"]["results"]):
        need(f"fixture.verification.results[{i}]", r, _RESULT_FIELDS)


# --------------------------------------------------------------------------- emitter


class _UnsupportedValueError(Exception):
    """A drafted spec with no DSL call; the emitter leaves it out and says so."""


def _py(value: Any) -> str:
    """Render a JSON value as a Python literal."""
    if value is None or isinstance(value, bool):
        return repr(value)
    if isinstance(value, str):
        return json.dumps(value)
    if isinstance(value, int | float):
        return repr(value)
    if isinstance(value, list):
        return "[" + ", ".join(_py(v) for v in value) + "]"
    if isinstance(value, dict):
        return "{" + ", ".join(f"{_py(k)}: {_py(v)}" for k, v in value.items()) + "}"
    raise _UnsupportedValueError(f"value {value!r}")


def _soft(spec: dict[str, Any]) -> str:
    return ", soft=True" if spec.get("soft") else ""


def dsl_call(contract: Contract) -> str:
    """Return the ``ExpectChain`` method call that emits ``contract``'s spec.

    Raises:
        ValueError: The spec has no Python DSL call.
    """
    s = contract.spec
    try:
        if contract.type == "trace":
            check = s.get("check")
            if check == "trajectory" and s.get("mode") != "partial_order":
                mode = s.get("mode", "strict")
                mode_arg = "" if mode == "strict" else f", mode={_py(mode)}"
                return f".trajectory({_py(s['tools'])}{mode_arg}{_soft(s)})"
            if check == "tool_order":
                return f".tool_called_before({_py(s['before'])}, {_py(s['after'])}{_soft(s)})"
            if check == "tool_cardinality":
                args = [_py(s["tool"])]
                if s.get("per"):
                    args.append(f"per={_py(s['per'])}")
                if s.get("min") is not None:
                    args.append(f"at_least={_py(s['min'])}")
                if s.get("max") is not None:
                    args.append(f"at_most={_py(s['max'])}")
                return f".tool_cardinality({', '.join(args)}{_soft(s)})"
        if contract.type == "schema" and s.get("target") == "output.structured":
            return f".output_matches_schema({_py(s['schema'])})"
        if (
            contract.type == "constraint"
            and s.get("field") == "metadata.cost_usd"
            and s.get("operator") == "lte"
        ):
            return f".cost_under({_py(s['value'])}{_soft(s)})"
    except (KeyError, _UnsupportedValueError) as exc:
        raise ValueError(f"contract {contract.id}: malformed spec ({exc})") from exc
    raise ValueError(f"contract {contract.id}: no Python DSL call for {contract.type} spec")


def _comment(text: str) -> str:
    """Make captured text safe inside a ``#`` comment: one line, no control characters."""
    return "".join(ch if ch.isprintable() else " " for ch in text)


def _identifier(name: str) -> str:
    ident = re.sub(r"\W", "_", name).strip("_").lower() or "fixture"
    if ident[0].isdigit() or keyword.iskeyword(ident):
        ident = "fixture_" + ident
    return ident


@dataclass(frozen=True)
class Rerun:
    """Where a drafted replay test re-runs the fixture's source command.

    ``cwd`` is the directory the capture ran in (the source command's relative paths
    resolve there) and ``world`` the world file the capture hosted, if any.
    """

    cwd: Path
    world: Path | None = None


def test_path_for(fixture_path: Path) -> Path:
    """Where the drafted test for a fixture goes: ``tests/attest/test_<name>.py`` for
    ``tests/attest/fixtures/<name>.attest.json``."""
    name = fixture_path.name.removesuffix(".attest.json")
    return fixture_path.parent.parent / f"test_{_identifier(name)}.py"


def _rel_expr(target: Path, test_path: Path) -> str:
    """A ``Path(__file__).parent / ...`` expression for ``target``, relative to the test."""
    rel = os.path.relpath(target, test_path.parent).replace(os.sep, "/")
    return "Path(__file__).parent" if rel == "." else f"Path(__file__).parent / {_py(rel)}"


def _contract_lines(calls: list[tuple[Contract, str]], subject: str) -> list[str]:
    lines = [f"        expect({subject})"]
    for c, call in calls:
        lines.append(f"        # {_comment(f'{c.id}: {c.description}')}")
        lines.append(f"        {call}")
    lines.append("    )")
    return lines


def _skipped_lines(skipped: list[str]) -> list[str]:
    if not skipped:
        return []
    return ["", "# Drafted contracts without a Python DSL call (left out):"] + [
        f"# - {_comment(s)}" for s in skipped
    ]


def _render_replay(
    fixture: Fixture,
    calls: list[tuple[Contract, str]],
    skipped: list[str],
    fixture_path: Path,
    test_path: Path,
    rerun: Rerun,
) -> str:
    ident = _identifier(fixture.name)
    world = _rel_expr(rerun.world, test_path) if rerun.world is not None else "None"
    if fixture.expect_fail:
        meaning = [
            "The recording is an incident: these contracts failed on it. The test passes once",
            "the new run satisfies them, so it fails again if the agent regresses.",
        ]
    else:
        meaning = ["The test fails when the agent's behavior stops satisfying them."]
    lines = [
        f'"""Replay test drafted by `attest capture` from fixture {_py(fixture.name)}.',
        "",
        "Re-runs the fixture's source command under strict replay with egress blocked (the",
        "engine's `replay --mode strict --no-live`): the recorded model responses answer the",
        "agent's calls, and a call that matches no recording fails the test. The drafted",
        "contracts are evaluated on the NEW run's trace, not on the recording.",
        *meaning,
        "",
        "A pass means the new run is consistent with the recording and satisfies the drafted",
        "contracts; it does not prove the agent correct. Identities are compared by role",
        "(relative identity): a run that swaps who plays which role misses, while the absolute",
        "identity is not checked. Review and edit before committing. Re-capturing does not",
        "overwrite this file.",
        '"""',
        "",
        "from __future__ import annotations",
        "",
        "from pathlib import Path",
        "",
        "from attest import ExpectChain, Trace, expect",
        "from attest.capture import replay_run",
        "from attest.plugin import AttestEngineFixture",
        "",
        f"FIXTURE = {_rel_expr(fixture_path, test_path)}",
        "# The directory `attest capture` ran in: the source command's relative paths start here.",
        f"RUN_DIR = {_rel_expr(rerun.cwd, test_path)}",
        f"WORLD = {world}",
        *_skipped_lines(skipped),
        "",
        "",
        "def contracts(trace: Trace) -> ExpectChain:",
        "    return (",
        *_contract_lines(calls, "trace"),
        "",
        "",
        f"def test_{ident}_new_run_consistent_with_recording(attest: AttestEngineFixture) -> None:",
        "    trace = replay_run(FIXTURE, cwd=RUN_DIR, world=WORLD)",
        "",
        "    attest.evaluate(contracts(trace))",
    ]
    return "\n".join(lines) + "\n"


def render_pytest(
    fixture: Fixture, fixture_path: Path, test_path: Path, rerun: Rerun | None = None
) -> str:
    """Render a fixture's drafted contracts as a pytest module.

    With ``rerun`` and a fixture captured from a command, the test re-runs that command
    under strict replay and evaluates every contract on the new run's trace (an incident
    reproduction included: it passes once the agent no longer reproduces the incident).
    Otherwise a verified fixture becomes a test that enforces every contract on the
    recorded trace with ``attest.evaluate``, and an incident reproduction (``expect_fail``)
    a test that checks, with ``attest.inspect``, that the expected failures still fail and
    the other contracts still pass; it stays green while it reproduces the incident.
    """
    calls: list[tuple[Contract, str]] = []
    skipped: list[str] = []
    for c in fixture.contracts:
        try:
            calls.append((c, dsl_call(c)))
        except ValueError as exc:
            skipped.append(str(exc))
    if rerun is not None and fixture.source["kind"] == "command" and fixture.replay_capable:
        return _render_replay(fixture, calls, skipped, fixture_path, test_path, rerun)
    ident = _identifier(fixture.name)
    status = {r.contract: r.status for r in fixture.results}

    lines = [
        f'"""Contracts drafted by `attest capture` from fixture {_py(fixture.name)}.',
        "",
        "Review and edit before committing: they check the captured trace without running",
        "the agent. Re-capturing does not overwrite this file.",
        '"""',
        "",
        "from __future__ import annotations",
        "",
        "from pathlib import Path",
        "",
        "from attest import ExpectChain, expect, load_fixture",
        "from attest.plugin import AttestEngineFixture",
        "",
        f"FIXTURE = load_fixture({_rel_expr(fixture_path, test_path)})",
        *_skipped_lines(skipped),
        "",
        "",
        "def contracts() -> ExpectChain:",
        "    return (",
        *_contract_lines(calls, "FIXTURE.trace"),
    ]

    if fixture.expect_fail:
        lines += [
            "",
            "",
            "# Status of each contract on the captured incident, in contracts() order.",
            "EXPECTED = [",
        ]
        lines += [f"    ({_py(c.id)}, {_py(status.get(c.id, 'pass'))})," for c, _ in calls]
        lines += [
            "]",
            "",
            "",
            f"def test_{ident}_reproduces_incident(attest: AttestEngineFixture) -> None:",
            "    result = attest.inspect(contracts())",
            "",
            "    statuses = [row.status for row in result.assertion_results]",
            "    assert statuses == [status for _, status in EXPECTED]",
        ]
    else:
        lines += [
            "",
            "",
            f"def test_{ident}_contracts_hold(attest: AttestEngineFixture) -> None:",
            "    attest.evaluate(contracts())",
        ]
    return "\n".join(lines) + "\n"


def emit_pytest(fixture_path: str | os.PathLike[str], rerun: Rerun | None = None) -> Path | None:
    """Write the drafted pytest file for a fixture unless it already exists.

    ``rerun`` makes the test of a command fixture re-run the command under strict replay
    (see :func:`render_pytest`).

    Returns the path written, or ``None`` when a test file is already there (re-capturing
    never overwrites an edited test).
    """
    fp = Path(fixture_path)
    test_path = test_path_for(fp)
    if test_path.is_symlink():
        raise CaptureError(f"{test_path} is a symbolic link; refusing to draft a test through it")
    if test_path.exists():
        return None
    source = render_pytest(load_fixture(fp), fp, test_path, rerun)
    test_path.parent.mkdir(parents=True, exist_ok=True)
    # Create exclusively and never through a link planted after the check.
    # Windows has no O_NOFOLLOW; O_EXCL there fails on any existing path.
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(test_path, flags, 0o644)
    except FileExistsError:
        raise CaptureError(f"{test_path} appeared while drafting; left unchanged") from None
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(source)
    return test_path


# ------------------------------------------------------------------------ replay_run()

_OTHER_PROVIDER_KEYS = ("ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY")
_REPLAY_TIMEOUT_S = 300.0
_STDERR_TAIL = 4000


def replay_run(
    fixture_path: str | os.PathLike[str],
    *,
    cwd: str | os.PathLike[str] | None = None,
    world: str | os.PathLike[str] | None = None,
    engine_path: str | None = None,
    timeout: float = _REPLAY_TIMEOUT_S,
) -> Trace:
    """Re-run a fixture's source command under strict replay and return the new run's trace.

    Runs ``attest-engine replay --mode strict --no-live --trace-out ... -- <command>``:
    the command's model calls are answered from the fixture's recorded exchanges, a call
    that matches no recording fails, and nothing reaches a provider (``--no-live``, the
    other providers' keys removed, ``OPENAI_API_KEY`` a placeholder, ``ATTEST_EGRESS=block``
    for any attest guard inside the command).

    Args:
        fixture_path: The fixture to replay; it must come from a command capture.
        cwd: Directory to run the command in (the one the capture ran in).
        world: World file the capture hosted, when the fixture has a ``world`` section.
        engine_path: Engine binary; default is the SDK's discovery order.
        timeout: Seconds before the run is abandoned.

    Raises:
        CaptureError: The fixture has no replayable source command, the engine could not
            start, the command failed, a request matched no recording, or no trace came out.
    """
    from attest.engine_manager import resolve_engine  # engine_manager imports the package

    fp = Path(fixture_path)
    fixture = load_fixture(fp)
    command = fixture.source.get("command")
    if fixture.source["kind"] != "command" or not fixture.replay_capable or not command:
        raise CaptureError(f"{fp}: not a command capture with recorded exchanges; cannot replay")
    if fixture.world is not None and world is None:
        raise CaptureError(f"{fp}: captured with a world; pass the world file the capture ran with")
    env = {k: v for k, v in os.environ.items() if k not in _OTHER_PROVIDER_KEYS}
    env["OPENAI_API_KEY"] = "replay-needs-no-key"
    env["ATTEST_EGRESS"] = "block"
    with tempfile.TemporaryDirectory(prefix="attest-replay-") as tmp:
        trace_file = Path(tmp) / "trace.json"
        argv = [
            resolve_engine(engine_path),
            "replay",
            "--fixture",
            str(fp.resolve()),
            "--mode",
            "strict",
            "--no-live",
            "--trace-out",
            str(trace_file),
        ]
        if world is not None:
            argv += ["--world", str(Path(world).resolve())]
        argv += ["--", *command]
        try:
            proc = subprocess.run(
                argv,
                cwd=cwd,
                env=env,
                capture_output=True,
                text=True,
                timeout=timeout,
                check=False,
            )
        except subprocess.TimeoutExpired:
            raise CaptureError(f"{fp}: replay run exceeded {timeout:g}s") from None
        except OSError as exc:
            raise CaptureError(f"{fp}: could not start the replay run: {exc}") from None
        if proc.returncode != 0:
            raise CaptureError(
                f"{fp}: replay run exited {proc.returncode}\n{proc.stderr[-_STDERR_TAIL:]}"
            )
        return Trace.from_dict(json.loads(trace_file.read_text(encoding="utf-8")))


# --------------------------------------------------------------------------- record()


@dataclass
class CaptureResult:
    """Outcome of a :func:`record` block, filled in when the block exits."""

    fixture: Path | None = None
    verified: bool | None = None
    #: ``verified``, ``not_verified`` or ``nothing_to_verify`` (a healthy run with no
    #: tool call, structured output or cost to draft a contract from: the fixture is
    #: written with ``verified`` False and no test is drafted).
    outcome: str | None = None
    test_file: Path | None = None


@dataclass
class Capture:
    """A running in-process capture.

    ``env`` holds the variables :func:`record` set in ``os.environ`` for the block;
    pass it to subprocesses started with an explicit environment.
    """

    name: str
    env: dict[str, str]
    result: CaptureResult = field(default_factory=CaptureResult)


def _flush_opentelemetry() -> None:
    """Flush the global OpenTelemetry tracer provider when the process uses one.

    Spans exported in batches must reach the receiver before the fixture is built.
    Only a provider the application already loaded is flushed.
    """
    otel_trace = sys.modules.get("opentelemetry.trace")
    if otel_trace is None:
        return
    provider = otel_trace.get_tracer_provider()
    force_flush = getattr(provider, "force_flush", None)
    if callable(force_flush) and force_flush() is False:
        raise CaptureError("OpenTelemetry tracer provider failed to flush its spans")


@contextmanager
def record(
    name: str,
    *,
    out_dir: str | os.PathLike[str] = DEFAULT_FIXTURE_DIR,
    expect_fail: bool = False,
    agent_id: str | None = None,
    emit_test: bool = True,
    engine_path: str | None = None,
    world: str | os.PathLike[str] | None = None,
) -> Iterator[Capture]:
    """Capture the code in the block as fixture ``<out_dir>/<name>.attest.json``.

    Starts ``attest-engine capture --serve``, which hosts the recording proxy and the
    OTLP/HTTP receiver on 127.0.0.1, and sets ``OPENAI_BASE_URL``, ``ANTHROPIC_BASE_URL``
    and ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`` for the block. Model clients and the
    OpenTelemetry exporter read these when they are constructed, so construct them inside
    the block. On exit the global tracer provider is flushed, the variables are restored
    and the engine writes the fixture; with ``emit_test`` the drafted pytest file is
    written next to the fixture directory when the fixture is verified, unless one exists.
    ``record()`` sets process-wide variables, so captures cannot overlap. If the block raises, the
    capture is abandoned and no fixture is written.

    Args:
        name: Fixture name (letters, digits, ``.``, ``_``, ``-``).
        out_dir: Fixture directory.
        expect_fail: Save an incident reproduction: verified when drafted contracts fail.
        agent_id: Trace ``agent_id`` (default: the first turn's agent name).
        emit_test: Write the drafted pytest file.
        engine_path: Engine binary (default: the SDK's engine discovery).
        world: A ``world.yaml`` to host for the block (the engine's ``--world``): its base
            URL variable (``PAYMENTS_BASE_URL``) is set for the block and the world's final
            state is saved in the fixture's ``world`` section.

    Raises:
        CaptureError: The engine could not write the fixture, or the fixture is not
            verified (a drafted contract did not hold, the trace check failed, or no model
            call was recorded; the fixture is still written). A healthy run with nothing to
            draft a contract from does not raise: ``capture.result.outcome`` is
            ``"nothing_to_verify"``, ``verified`` is False and no test is drafted.
    """
    # attest.engine_manager imports the attest package, which imports this module.
    from attest.engine_manager import resolve_engine

    if not _RECORDING.acquire(blocking=False):
        raise CaptureError("record() is already running; captures cannot overlap")
    try:
        with _serve(
            name,
            resolve_engine(engine_path),
            os.fspath(out_dir),
            expect_fail,
            agent_id,
            None if world is None else os.fspath(world),
        ) as capture:
            yield capture
    finally:
        _RECORDING.release()
    if capture.result.outcome == "not_verified":
        raise CaptureError(
            f"capture {name}: fixture {capture.result.fixture} is not verified; no test drafted"
        )
    if emit_test and capture.result.verified and capture.result.fixture is not None:
        capture.result.test_file = emit_pytest(capture.result.fixture)


@contextmanager
def _serve(
    name: str,
    engine: str,
    out_dir: str,
    expect_fail: bool,
    agent_id: str | None,
    world: str | None,
) -> Iterator[Capture]:
    """Run ``attest-engine capture --serve`` around the block and fill in the result."""
    args = [engine, "capture", "--serve", "--name", name, "--out-dir", out_dir]
    if expect_fail:
        args.append("--expect-fail")
    if agent_id is not None:
        args += ["--agent-id", agent_id]
    if world is not None:
        args += ["--world", world]
    proc = subprocess.Popen(
        args,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    try:
        env = _ready_env(proc.stdout.readline())
    except CaptureError:
        _, err = _finish(proc, finalize=False)
        raise CaptureError(f"attest-engine capture --serve did not start: {err.strip()}") from None
    capture = Capture(name=name, env=env)
    saved = {k: os.environ.get(k) for k in env}
    os.environ.update(env)
    try:
        yield capture
        _flush_opentelemetry()
    except BaseException:
        _restore(saved)
        _finish(proc, finalize=False)
        raise
    _restore(saved)
    out, err = _finish(proc, finalize=True)
    sys.stderr.write(err)
    if not out.strip():
        raise CaptureError(f"capture {name} wrote no fixture: {err.strip()}")
    outcome = json.loads(out.strip().splitlines()[-1])
    capture.result.fixture = Path(outcome["fixture"])
    capture.result.verified = proc.returncode == 0 and bool(outcome["verified"])
    capture.result.outcome = outcome["outcome"] if proc.returncode == 0 else "not_verified"


# One capture at a time: record() sets process-wide environment variables, so a
# second concurrent capture would save and later restore the first one's values.
_RECORDING = threading.Lock()


def _ready_env(line: str) -> dict[str, str]:
    """Parse the engine's ready line, ``{"env": {...}}``."""
    try:
        env = json.loads(line)["env"]
    except (ValueError, KeyError, TypeError):
        raise CaptureError(f"unexpected ready line {line.strip()[:200]!r}") from None
    if not isinstance(env, dict) or not all(isinstance(v, str) for v in env.values()):
        raise CaptureError(f"unexpected ready line {line.strip()[:200]!r}")
    return env


def _finish(proc: subprocess.Popen[str], *, finalize: bool) -> tuple[str, str]:
    """Close the engine's stdin (after ``finalize`` when asked) and reap it.

    An engine that already exited cannot take ``finalize``; its output still says why.
    """
    stdin = proc.stdin
    assert stdin is not None
    try:
        if finalize:
            # The environment as it is now: secrets the block loaded (a dotenv
            # file, for example) are redacted too.
            environ = [f"{k}={v}" for k, v in os.environ.items()]
            stdin.write(json.dumps({"finalize": True, "environ": environ}) + "\n")
        stdin.close()
    except BrokenPipeError:
        pass
    # communicate() flushes stdin when it is set; it is closed (or broken) here.
    proc.stdin = None
    return proc.communicate()


def _restore(saved: dict[str, str | None]) -> None:
    for k, v in saved.items():
        if v is None:
            os.environ.pop(k, None)
        else:
            os.environ[k] = v
