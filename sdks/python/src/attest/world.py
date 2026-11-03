"""World: a stateful HTTP stand-in for the system an agent acts on.

``attest-engine world --file world.yaml --serve`` hosts the world on 127.0.0.1 and prints
the environment variable (for example ``PAYMENTS_BASE_URL``) the agent's HTTP client reads.
:func:`start_world` starts it and sets that variable. When the session stops the engine
prints the final world-v1 snapshot; :meth:`WorldSession.state` reads the current one while
it runs. Pass either to :meth:`attest.expect.ExpectChain.world` to assert on the effects.
In the pytest plugin the ``attest_world`` fixture wraps this; it composes with
``attest_replay``.
"""

from __future__ import annotations

import json
import os
import subprocess
import threading
import urllib.error
import urllib.request
from pathlib import Path
from types import TracebackType
from typing import Any

from attest.capture import CaptureError, _ready_env, _restore
from attest.exceptions import AttestError
from attest.replay import sanitize

# The path under the world's base URL that serves the current snapshot (world.StatePath).
STATE_PATH = "/_attest/state"
_STATE_TIMEOUT_S = 10
_DIRECT = urllib.request.build_opener(urllib.request.ProxyHandler({}))


class WorldError(AttestError):
    """The world engine did not start, did not answer, or did not report a snapshot."""


# One world at a time: it sets process-wide environment variables.
_WORLD_RUNNING = threading.Lock()


class WorldSession:
    """A running world. Use as a context manager, or call :meth:`stop`."""

    def __init__(
        self,
        path: str,
        env: dict[str, str],
        proc: subprocess.Popen[str],
        saved: dict[str, str | None],
    ) -> None:
        self.path = path
        self.env = env
        self._proc = proc
        self._saved = saved
        self._stopped = False
        self._final: dict[str, Any] | None = None

    @property
    def base_url(self) -> str:
        """The world's base URL: the value of its single environment variable."""
        [url] = self.env.values()
        return url

    def __enter__(self) -> WorldSession:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        self.stop()

    def state(self) -> dict[str, Any]:
        """The current world-v1 snapshot, read from the running world.

        Raises:
            WorldError: The world is stopped, unreachable, or did not answer 200 with JSON.
        """
        if self._stopped:
            raise WorldError(f"world {self.path} is stopped; use the snapshot stop() returned")
        url = self.base_url + STATE_PATH
        try:
            # The world is on loopback: never route it through an environment proxy.
            with _DIRECT.open(url, timeout=_STATE_TIMEOUT_S) as resp:
                status, body = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            raise WorldError(f"GET {url}: HTTP {e.code}") from None
        except OSError as e:
            raise WorldError(f"GET {url}: {e}") from None
        if status != 200:
            raise WorldError(f"GET {url}: HTTP {status}")
        try:
            snapshot = json.loads(body)
        except ValueError:
            raise WorldError(f"GET {url}: the body is not JSON") from None
        if not isinstance(snapshot, dict):
            raise WorldError(f"GET {url}: the body is not a snapshot object")
        return snapshot

    def stop(self) -> dict[str, Any]:
        """Stop the world, restore the environment and return the final snapshot.
        Idempotent: later calls return the same snapshot."""
        if self._final is not None:
            return self._final
        if self._stopped:
            raise WorldError(f"world {self.path} already failed to stop")
        self._stopped = True
        proc = self._proc
        assert proc.stdin is not None
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
            _WORLD_RUNNING.release()
        lines = [ln for ln in out.splitlines() if ln.strip()]
        if proc.returncode != 0 or not lines:
            raise WorldError(sanitize(f"attest-engine world {self.path} failed: {err.strip()}"))
        try:
            final = json.loads(lines[-1])["world"]
        except (ValueError, KeyError, TypeError):
            raise WorldError(f"unexpected world output {lines[-1][:200]!r}") from None
        if not isinstance(final, dict):
            raise WorldError(f"unexpected world output {lines[-1][:200]!r}")
        self._final = final
        return final


def start_world(path: str | os.PathLike[str], *, engine_path: str | None = None) -> WorldSession:
    """Host the world defined by ``path`` (a ``world.yaml``) until the session stops.

    Sets the world's base URL variable (``PAYMENTS_BASE_URL`` for a ``payments`` world)
    until the session stops. HTTP clients read it when they are constructed, so construct
    them after this call. Composes with :func:`attest.replay.start_replay`: they run as two
    engine processes and their variables merge.

    Raises:
        WorldError: The engine did not start (a missing or malformed world file) or another
            world is running.
    """
    # attest.engine_manager imports the attest package, which imports this module.
    from attest.engine_manager import resolve_engine

    world_file = os.fspath(Path(path))
    if not _WORLD_RUNNING.acquire(blocking=False):
        raise WorldError("a world is already running; worlds cannot overlap")
    try:
        proc = subprocess.Popen(
            [resolve_engine(engine_path), "world", "--file", world_file, "--serve"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
    except BaseException:
        _WORLD_RUNNING.release()
        raise
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    try:
        env = _ready_env(proc.stdout.readline())
    except CaptureError:
        proc.stdin.close()
        proc.stdin = None
        _, err = proc.communicate()
        _WORLD_RUNNING.release()
        raise WorldError(
            sanitize(f"attest-engine world {world_file} did not start: {err.strip()}")
        ) from None
    except BaseException:
        proc.kill()
        proc.communicate()
        _WORLD_RUNNING.release()
        raise
    if len(env) != 1:
        proc.kill()
        proc.communicate()
        _WORLD_RUNNING.release()
        raise WorldError(f"attest-engine world {world_file}: want one env variable, got {env}")
    saved = {k: os.environ.get(k) for k in env}
    os.environ.update(env)
    return WorldSession(world_file, env, proc, saved)
