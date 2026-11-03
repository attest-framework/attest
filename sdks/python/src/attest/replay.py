"""Replay: run an agent's real code against a fixture's recorded model responses.

``attest-engine replay --serve`` hosts a proxy on 127.0.0.1 that answers OpenAI Chat
Completions, OpenAI Responses and Anthropic Messages requests from the exchanges recorded
by ``attest capture`` / :func:`attest.record`. :func:`start_replay` starts it and points
``OPENAI_BASE_URL``, ``ANTHROPIC_BASE_URL`` and ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`` at
it. In the pytest plugin the ``attest_replay`` fixture wraps this.

A request that matches no recording is a *miss*. ``strict`` fails the test with a message
naming the first component that changed; ``auto`` goes live when a provider key is set and
otherwise leaves the test ``not_evaluated`` with reason ``replay_miss``.
"""

from __future__ import annotations

import json
import os
import subprocess
import threading
import unicodedata
from dataclasses import dataclass, field
from pathlib import Path
from types import TracebackType
from typing import Any

from attest import egress
from attest.capture import DEFAULT_FIXTURE_DIR, CaptureError, _ready_env
from attest.exceptions import AttestError

MODE_STRICT = "strict"
MODE_AUTO = "auto"
MODE_OFF = "off"
REPLAY_MODES = (MODE_STRICT, MODE_AUTO, MODE_OFF)


def sanitize(text: str) -> str:
    """Escape control and format characters (Unicode Cc and Cf, newlines included) as
    ``\\uXXXX``, so text taken from a fixture or the engine cannot inject terminal escapes
    or CI log commands."""
    return "".join(
        f"\\u{ord(c):04x}" if unicodedata.category(c) in ("Cc", "Cf") else c for c in text
    )


class ReplayError(AttestError):
    """The replay engine could not start, or ended abnormally."""


class ReplayMissError(AttestError):
    """A strict replay received a request with no recording. ``str()`` is the miss message."""


class ReplayNotEvaluatedError(AttestError):
    """An auto replay received a request with no recording and no provider key was set:
    the test is ``not_evaluated`` with reason ``replay_miss``."""


@dataclass(frozen=True)
class ReplayRecord:
    """The outcome of one model request the agent made."""

    seq: int
    provider: str
    api: str
    outcome: str  # "replayed" | "live" | "miss"
    exchange: int | None = None
    message: str | None = None


@dataclass(frozen=True)
class ReplaySummary:
    """What one replay session served, as printed by the engine when it stops."""

    fixture: str
    mode: str
    replayed: int
    live: int
    misses: int
    unused: int
    live_cost_usd: float
    unpriced_live_calls: int
    records: tuple[ReplayRecord, ...] = field(default=())

    @property
    def first_miss(self) -> str | None:
        """The message of the first request that matched no recording."""
        for rec in self.records:
            if rec.outcome == "miss":
                return rec.message
        return None

    @classmethod
    def from_dict(cls, doc: dict[str, Any]) -> ReplaySummary:
        """Parse the engine's ``summary`` object."""
        try:
            records = tuple(
                ReplayRecord(
                    seq=int(r["seq"]),
                    provider=str(r["provider"]),
                    api=str(r["api"]),
                    outcome=str(r["outcome"]),
                    exchange=r.get("exchange"),
                    message=sanitize(r["message"]) if r.get("message") is not None else None,
                )
                for r in doc["records"]
            )
            return cls(
                fixture=str(doc["fixture"]),
                mode=str(doc["mode"]),
                replayed=int(doc["replayed"]),
                live=int(doc["live"]),
                misses=int(doc["misses"]),
                unused=int(doc["unused"]),
                live_cost_usd=float(doc["live_cost_usd"]),
                unpriced_live_calls=int(doc["unpriced_live_calls"]),
                records=records,
            )
        except (KeyError, TypeError, ValueError) as exc:
            raise ReplayError(f"unexpected replay summary: {exc!r}") from None

    def to_report(self) -> dict[str, Any]:
        """The ``tests[].replay`` object of report-v2 for this session."""
        row: dict[str, Any] = {
            "fixtures": [self.fixture],
            "mode": self.mode,
            "replayed": self.replayed,
            "live": self.live,
            "misses": self.misses,
        }
        if self.first_miss is not None:
            row["miss"] = self.first_miss
        return row


def first_miss(summary: ReplaySummary) -> str | None:
    """The message of the first request that matched no recording."""
    return summary.first_miss


def live_line(live: int, cost_usd: float, unpriced: int) -> str:
    """``"N live model calls · $X.XX"``, the replay summary line shared with the engine."""
    line = f"{live} live model {'call' if live == 1 else 'calls'} · ${cost_usd:.2f}"
    if unpriced:
        line += f" (+{unpriced} unpriced)"
    return line


def resolve_mode(explicit: str | None = None) -> str:
    """The replay mode: ``explicit``, else ``ATTEST_REPLAY``, else ``strict`` when ``CI`` is
    set and ``auto`` locally."""
    mode = explicit or os.environ.get("ATTEST_REPLAY", "").strip().lower() or None
    if mode is None:
        ci = os.environ.get("CI", "").strip().lower()
        mode = MODE_STRICT if ci not in ("", "false", "0") else MODE_AUTO
    if mode not in REPLAY_MODES:
        raise ReplayError(f"replay mode {mode!r}: want one of {', '.join(REPLAY_MODES)}")
    return mode


# One replay at a time: it sets process-wide environment variables.
_REPLAYING = threading.Lock()


class ReplaySession:
    """A running replay. Use as a context manager, or call :meth:`finish`.

    Leaving the ``with`` block (or calling :meth:`check`) raises :class:`ReplayMissError`
    when a strict replay missed, and :class:`ReplayNotEvaluatedError` when an auto replay
    missed without a provider key, whatever the block raised itself: an agent fails in
    unrelated ways when its model call is refused, and the miss message is the cause.
    """

    def __init__(
        self,
        name: str,
        mode: str,
        env: dict[str, str],
        proc: subprocess.Popen[str] | None,
        saved: dict[str, str | None],
        no_live: bool = False,
    ) -> None:
        self.name = name
        self.mode = mode
        self.env = env
        self.summary: ReplaySummary | None = None
        self._proc = proc
        self._saved = saved
        self._finished = proc is None
        self._no_live = no_live

    def __enter__(self) -> ReplaySession:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        self.finish()
        error = self.error()
        if error is not None:
            raise error from exc

    def finish(self) -> ReplaySummary | None:
        """Stop the engine, restore the environment and return the summary. Idempotent;
        ``None`` for ``off`` mode."""
        if self._finished:
            return self.summary
        self._finished = True
        proc = self._proc
        assert proc is not None and proc.stdin is not None
        _restore(self._saved)
        try:
            try:
                proc.stdin.write("stop\n")
                proc.stdin.close()
            except OSError:  # the engine already exited; its output says why
                pass
            proc.stdin = None
            try:
                out, err = proc.communicate()
            except BaseException:
                proc.kill()
                proc.communicate()
                raise
        finally:
            _REPLAYING.release()
        lines = [ln for ln in out.splitlines() if ln.strip()]
        if proc.returncode != 0 or not lines:
            raise ReplayError(sanitize(f"attest-engine replay {self.name} failed: {err.strip()}"))
        try:
            doc = json.loads(lines[-1])["summary"]
        except (ValueError, KeyError, TypeError):
            raise ReplayError(f"unexpected replay output {lines[-1][:200]!r}") from None
        self.summary = ReplaySummary.from_dict(doc)
        if self._no_live and self.summary.live > 0:
            raise ReplayError(
                f"replay {self.name}: {self.summary.live} live model call(s) while the egress "
                "guard was blocking; a blocked run must make none"
            )
        return self.summary

    def error(self) -> AttestError | None:
        """The exception a miss raises, or ``None``. Valid after :meth:`finish`."""
        if self.summary is None or self.summary.misses == 0:
            return None
        message = self.summary.first_miss or "replay miss"
        if self.mode == MODE_AUTO:
            return ReplayNotEvaluatedError(message)
        return ReplayMissError(message)

    def check(self) -> None:
        """Finish and raise the miss error, if any."""
        self.finish()
        error = self.error()
        if error is not None:
            raise error


def start_replay(
    name: str,
    *,
    mode: str | None = None,
    fixtures_dir: str | os.PathLike[str] = DEFAULT_FIXTURE_DIR,
    engine_path: str | None = None,
) -> ReplaySession:
    """Start replaying fixture ``<fixtures_dir>/<name>.attest.json``.

    Sets ``OPENAI_BASE_URL``, ``ANTHROPIC_BASE_URL`` and ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT``
    until the session finishes. Model clients read them when constructed, so construct them
    after this call. Mode ``off`` starts nothing and leaves the environment alone.

    Args:
        name: Fixture name.
        mode: ``strict``, ``auto`` or ``off`` (default: :func:`resolve_mode`).
        fixtures_dir: Fixture directory.
        engine_path: Engine binary (default: the SDK's engine discovery).

    Raises:
        ReplayError: The engine did not start (a missing fixture, a trace-only fixture).
    """
    # attest.engine_manager imports the attest package, which imports this module.
    from attest.engine_manager import resolve_engine

    resolved = resolve_mode(mode)
    no_live = egress.active() or egress.egress_block_requested()
    if resolved == MODE_OFF:
        return ReplaySession(name, MODE_OFF, {}, None, {})
    if not _REPLAYING.acquire(blocking=False):
        raise ReplayError("a replay is already running; replays cannot overlap")
    try:
        proc = subprocess.Popen(
            [
                resolve_engine(engine_path),
                "replay",
                "--serve",
                "--name",
                name,
                "--fixtures-dir",
                os.fspath(fixtures_dir),
                "--mode",
                resolved,
                *(["--no-live"] if no_live else []),
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
    except BaseException:
        _REPLAYING.release()
        raise
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    try:
        env = _ready_env(proc.stdout.readline())
    except CaptureError:
        proc.stdin.close()
        proc.stdin = None
        _, err = proc.communicate()
        _REPLAYING.release()
        raise ReplayError(
            sanitize(f"attest-engine replay {name} did not start: {err.strip()}")
        ) from None
    except BaseException:
        proc.kill()
        proc.communicate()
        _REPLAYING.release()
        raise
    saved = {k: os.environ.get(k) for k in env}
    os.environ.update(env)
    return ReplaySession(name, resolved, env, proc, saved, no_live)


def _restore(saved: dict[str, str | None]) -> None:
    for key, value in saved.items():
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value


def fixtures_dir_for(root: Path, explicit: str | os.PathLike[str] | None) -> Path:
    """``explicit`` as given, else ``<root>/tests/attest/fixtures``."""
    return Path(explicit) if explicit is not None else root / DEFAULT_FIXTURE_DIR
