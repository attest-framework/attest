"""Capture: turn a run into a verified fixture, and fixtures into tests.

``attest capture --name <name> -- <command>`` (the engine) records a run's trace and
model exchanges into ``tests/attest/fixtures/<name>.attest.json``. This module is the
Python side:

- :func:`record` captures the code inside a ``with`` block of this process.
- :func:`load_fixture` reads a fixture (``proto/attest/v1/fixture-v1.schema.json``).
- :func:`emit_pytest` writes the fixture's drafted contracts as a pytest file, using the
  ``expect()`` DSL. ``attest capture`` and :func:`record` call it.
"""

from __future__ import annotations

import json
import keyword
import os
import re
import subprocess
import sys
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


def _identifier(name: str) -> str:
    ident = re.sub(r"\W", "_", name).strip("_").lower() or "fixture"
    if ident[0].isdigit() or keyword.iskeyword(ident):
        ident = "fixture_" + ident
    return ident


def test_path_for(fixture_path: Path) -> Path:
    """Where the drafted test for a fixture goes: ``tests/attest/test_<name>.py`` for
    ``tests/attest/fixtures/<name>.attest.json``."""
    name = fixture_path.name.removesuffix(".attest.json")
    return fixture_path.parent.parent / f"test_{_identifier(name)}.py"


def render_pytest(fixture: Fixture, fixture_path: Path, test_path: Path) -> str:
    """Render a fixture's drafted contracts as a pytest module.

    A verified fixture becomes a test that enforces every contract with
    ``attest.evaluate``. An incident reproduction (``expect_fail``) becomes a test that
    checks, with ``attest.inspect``, that the expected failures still fail and the other
    contracts still pass; it stays green while it reproduces the incident.
    """
    calls: list[tuple[Contract, str]] = []
    skipped: list[str] = []
    for c in fixture.contracts:
        try:
            calls.append((c, dsl_call(c)))
        except ValueError as exc:
            skipped.append(str(exc))
    ident = _identifier(fixture.name)
    rel = os.path.relpath(fixture_path, test_path.parent).replace(os.sep, "/")
    status = {r.contract: r.status for r in fixture.results}

    lines = [
        f'"""Contracts drafted by `attest capture` from fixture {fixture.name}.',
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
        f"FIXTURE = load_fixture(Path(__file__).parent / {_py(rel)})",
    ]
    if skipped:
        lines += ["", "# Drafted contracts without a Python DSL call (left out):"]
        lines += [f"# - {s}" for s in skipped]
    lines += ["", "", "def contracts() -> ExpectChain:", "    return ("]
    lines.append("        expect(FIXTURE.trace)")
    for c, call in calls:
        lines.append(f"        # {c.id}: {c.description}")
        lines.append(f"        {call}")
    lines.append("    )")

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


def emit_pytest(fixture_path: str | os.PathLike[str]) -> Path | None:
    """Write the drafted pytest file for a fixture unless it already exists.

    Returns the path written, or ``None`` when a test file is already there (re-capturing
    never overwrites an edited test).
    """
    fp = Path(fixture_path)
    test_path = test_path_for(fp)
    if test_path.exists():
        return None
    source = render_pytest(load_fixture(fp), fp, test_path)
    test_path.parent.mkdir(parents=True, exist_ok=True)
    test_path.write_text(source, encoding="utf-8")
    return test_path


# --------------------------------------------------------------------------- record()


@dataclass
class CaptureResult:
    """Outcome of a :func:`record` block, filled in when the block exits."""

    fixture: Path | None = None
    verified: bool | None = None
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
) -> Iterator[Capture]:
    """Capture the code in the block as fixture ``<out_dir>/<name>.attest.json``.

    Starts ``attest-engine capture --serve``, which hosts the recording proxy and the
    OTLP/HTTP receiver on 127.0.0.1, and sets ``OPENAI_BASE_URL``, ``ANTHROPIC_BASE_URL``
    and ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`` for the block. Model clients and the
    OpenTelemetry exporter read these when they are constructed, so construct them inside
    the block. On exit the global tracer provider is flushed, the variables are restored
    and the engine writes the fixture; with ``emit_test`` the drafted pytest file is
    written next to the fixture directory unless one exists. If the block raises, the
    capture is abandoned and no fixture is written.

    Args:
        name: Fixture name (letters, digits, ``.``, ``_``, ``-``).
        out_dir: Fixture directory.
        expect_fail: Save an incident reproduction: verified when drafted contracts fail.
        agent_id: Trace ``agent_id`` (default: the first turn's agent name).
        emit_test: Write the drafted pytest file.
        engine_path: Engine binary (default: the SDK's engine discovery).

    Raises:
        CaptureError: The engine could not write the fixture, or the fixture is not
            verified (its drafted contracts did not hold; the fixture is still written).
    """
    # attest.engine_manager imports the attest package, which imports this module.
    from attest.engine_manager import resolve_engine

    engine = resolve_engine(engine_path)
    args = [engine, "capture", "--serve", "--name", name, "--out-dir", os.fspath(out_dir)]
    if expect_fail:
        args.append("--expect-fail")
    if agent_id is not None:
        args += ["--agent-id", agent_id]
    proc = subprocess.Popen(
        args,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    ready = proc.stdout.readline()
    if not ready:
        proc.stdin.close()
        stderr = proc.stderr.read()
        proc.wait()
        raise CaptureError(f"attest-engine capture --serve did not start: {stderr.strip()}")
    env: dict[str, str] = json.loads(ready)["env"]
    capture = Capture(name=name, env=env)
    saved = {k: os.environ.get(k) for k in env}
    os.environ.update(env)
    try:
        yield capture
        _flush_opentelemetry()
    except BaseException:
        _restore(saved)
        proc.stdin.close()
        proc.communicate()
        raise
    _restore(saved)
    proc.stdin.write("finalize\n")
    proc.stdin.close()
    out, err = proc.communicate()
    sys.stderr.write(err)
    if not out.strip():
        raise CaptureError(f"capture {name} wrote no fixture: {err.strip()}")
    outcome = json.loads(out.strip().splitlines()[-1])
    capture.result.fixture = Path(outcome["fixture"])
    capture.result.verified = bool(outcome["verified"])
    if emit_test:
        capture.result.test_file = emit_pytest(capture.result.fixture)
    if proc.returncode != 0:
        raise CaptureError(f"capture {name}: fixture {capture.result.fixture} is not verified")


def _restore(saved: dict[str, str | None]) -> None:
    for k, v in saved.items():
        if v is None:
            os.environ.pop(k, None)
        else:
            os.environ[k] = v
