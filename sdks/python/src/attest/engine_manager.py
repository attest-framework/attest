"""Engine subprocess lifecycle manager."""

from __future__ import annotations

import asyncio
import json
import logging
import os
import subprocess
import time
from collections.abc import Mapping
from typing import TYPE_CHECKING, Any

from attest import ENGINE_VERSION, __version__
from attest._proto.codec import (
    ProtocolError,
    decode_response,
    encode_request,
    extract_id,
    extract_result,
)
from attest._proto.types import InitializeParams, InitializeResult
from attest.engine_downloader import _binary_filename, cached_engine_path, download_engine
from attest.exceptions import EngineTimeoutError, EngineVersionMismatchError

if TYPE_CHECKING:
    from attest.client import AttestClient

logger = logging.getLogger("attest.engine")

ENGINE_BINARY_NAME = "attest-engine"

_DEFAULT_ENGINE_TIMEOUT: float = 30.0

_VERSION_PROBE_TIMEOUT: float = 10.0

# Equal to the engine's request line cap (``MaxLineBytes`` in
# engine/internal/server/server.go), which bounds what the engine reads, not
# what it writes. asyncio's default 64 KiB stream limit makes ``readline``
# raise ValueError on a longer response line, so the SDK raises its own limit
# to a size no realistic evaluate_batch response exceeds.
MAX_FRAME_BYTES: int = 10 * 1024 * 1024


def _engine_timeout() -> float:
    """Read engine response timeout from ATTEST_ENGINE_TIMEOUT env var.

    Returns the default (30 s) when the variable is unset or unparseable.
    """
    raw = os.environ.get("ATTEST_ENGINE_TIMEOUT", "")
    if raw:
        try:
            return float(raw)
        except ValueError:
            logger.warning(
                "ATTEST_ENGINE_TIMEOUT=%r is not a valid float; using default %.0fs",
                raw,
                _DEFAULT_ENGINE_TIMEOUT,
            )
    return _DEFAULT_ENGINE_TIMEOUT


def _is_download_disabled() -> bool:
    """Check if auto-download is disabled via ATTEST_ENGINE_NO_DOWNLOAD."""
    val = os.environ.get("ATTEST_ENGINE_NO_DOWNLOAD", "").lower()
    return val in ("1", "true", "yes")


def engine_version(path: str) -> str | None:
    """Return the version ``<path> version`` reports, or ``None`` if it reports none.

    Every engine since 0.4 answers ``version`` with ``attest-engine <version>``. stdin is
    closed so a binary that does not know the subcommand and starts the JSON-RPC server
    instead reads EOF and exits.
    """
    proc = subprocess.run(
        [path, "version"],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=_VERSION_PROBE_TIMEOUT,
        check=False,
    )
    prefix = f"{ENGINE_BINARY_NAME} "
    out = proc.stdout.strip()
    if proc.returncode != 0 or not out.startswith(prefix):
        return None
    return out[len(prefix) :].strip()


def _on_path() -> str | None:
    """Return the first executable engine binary in a ``PATH`` directory.

    Only the platform binary name is considered (``attest-engine.exe`` on Windows, no other
    ``PATHEXT`` extension) and empty ``PATH`` entries are skipped, so the current directory
    is never searched implicitly.
    """
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        if not directory:
            continue
        candidate = os.path.join(directory, _binary_filename())
        if os.path.isfile(candidate) and os.access(candidate, os.X_OK):
            return candidate
    return None


def _locate_engine(explicit_path: str | None, allow_download: bool) -> tuple[str, str]:
    """Return ``(path, source)`` for the first engine in the discovery chain."""
    if explicit_path:
        if not os.path.isfile(explicit_path):
            raise FileNotFoundError(f"Engine path {explicit_path} does not exist or is not a file.")
        return explicit_path, "explicit engine path"

    env_path = os.environ.get("ATTEST_ENGINE_PATH")
    if env_path:
        if not os.path.isfile(env_path):
            raise FileNotFoundError(
                f"ATTEST_ENGINE_PATH={env_path} does not exist or is not a file."
            )
        return env_path, "ATTEST_ENGINE_PATH"

    found = _on_path()
    if found:
        return found, "PATH"

    cached = cached_engine_path()
    if cached is not None:
        return str(cached), "engine cache"

    if not allow_download:
        raise FileNotFoundError(
            f"No '{ENGINE_BINARY_NAME}' {ENGINE_VERSION} in ATTEST_ENGINE_PATH, PATH or "
            f"~/.attest/bin/{ENGINE_VERSION}/."
        )
    if _is_download_disabled():
        raise FileNotFoundError(
            f"Cannot find '{ENGINE_BINARY_NAME}' {ENGINE_VERSION} and "
            "ATTEST_ENGINE_NO_DOWNLOAD is set.\n"
            "Install options:\n"
            "  1. Build from source: make engine, then set ATTEST_ENGINE_PATH\n"
            f"  2. Download release v{ENGINE_VERSION}: "
            "https://github.com/attest-framework/attest/releases\n"
            "  3. Allow auto-download by unsetting ATTEST_ENGINE_NO_DOWNLOAD"
        )
    return str(download_engine()), "download"


def resolve_engine(explicit_path: str | None = None, *, allow_download: bool = True) -> str:
    """Find an engine whose version matches this SDK and return its path.

    The single discovery function behind the pytest fixture, ``EngineManager`` and the
    ``attest`` CLI. Discovery order:

        1. ``explicit_path`` (``--attest-engine`` / ``EngineManager(engine_path=...)``)
        2. ``ATTEST_ENGINE_PATH``
        3. ``attest-engine`` on ``PATH``
        4. ``~/.attest/bin/<ENGINE_VERSION>/`` cache
        5. Download ``ENGINE_VERSION`` from GitHub Releases (unless
           ``ATTEST_ENGINE_NO_DOWNLOAD`` is set or ``allow_download`` is False)

    The first engine found must report exactly ``ENGINE_VERSION``; discovery never skips a
    mismatched engine to try the next location.

    Raises:
        FileNotFoundError: No engine was found and download is disabled.
        EngineVersionMismatchError: The engine found reports a different version.
    """
    path, source = _locate_engine(explicit_path, allow_download)
    found = engine_version(path)
    if found != ENGINE_VERSION:
        raise EngineVersionMismatchError(path, source, found, ENGINE_VERSION)
    return path


def _line_id(line: bytes) -> int | None:
    """The integer ``id`` of a JSON-RPC line, or None when absent or unparseable."""
    try:
        data: Any = json.loads(line)
        return int(data["id"])
    except (ValueError, KeyError, TypeError):
        return None


class EngineManager:
    """Manages the lifecycle of the attest-engine subprocess.

    The manager owns the process and its ``initialize`` / ``shutdown``
    handshake. Every other request goes through ``AttestClient``, whose
    reader routes each response to its request by ``id``.
    """

    def __init__(
        self,
        engine_path: str | None = None,
        log_level: str = "warn",
    ) -> None:
        self._engine_path = resolve_engine(engine_path)
        self._log_level = log_level
        self._process: asyncio.subprocess.Process | None = None
        self._initialized = False
        self._request_id = 0
        self._init_result: InitializeResult | None = None
        self._spawn_env: dict[str, str] = dict(os.environ)
        self._client: AttestClient | None = None

    @property
    def spawn_env(self) -> Mapping[str, str]:
        """The environment the current engine process was started with."""
        return self._spawn_env

    def attach_client(self, client: AttestClient) -> None:
        """Register the ``AttestClient`` whose reader owns stdout while it runs."""
        self._client = client

    async def send_request(self, method: str, params: dict[str, Any]) -> Any:
        """Send a request and return the result of its own response.

        With an attached ``AttestClient`` the request goes through it (id
        correlation, timeout restart). Without one it uses the handshake read,
        which drops every line that does not carry the request's id.
        """
        if self._client is not None:
            return await self._client.send_request(method, params)
        return await self._handshake(method, params)

    async def start(self, env_overrides: Mapping[str, str] | None = None) -> InitializeResult:
        """Start the engine subprocess and send initialize.

        ``env_overrides`` is layered over this process's environment for the
        engine only. Any failure after the process starts stops it before the
        error propagates.
        """
        self._spawn_env = {**os.environ, **(env_overrides or {})}
        self._process = await asyncio.create_subprocess_exec(
            self._engine_path,
            f"--log-level={self._log_level}",
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=self._spawn_env,
            limit=MAX_FRAME_BYTES,
        )
        logger.info("Engine started (pid=%d)", self._process.pid)
        try:
            self._init_result = await self._initialize()
        except BaseException:
            await self.stop()
            raise
        self._initialized = True
        return self._init_result

    async def restart(self, env_overrides: Mapping[str, str] | None = None) -> InitializeResult:
        """Kill the current engine and start a fresh one.

        Used after a request timeout: the old process may still hold the
        response, so it is killed rather than asked to shut down.
        """
        await self._kill()
        return await self.start(env_overrides)

    async def _initialize(self) -> InitializeResult:
        """Send initialize and check the engine's version and capabilities."""
        result = await self._handshake(
            "initialize",
            InitializeParams(
                sdk_name="attest-python",
                sdk_version=__version__,
                protocol_version=1,
                required_capabilities=["layers_1_4"],
            ).to_dict(),
            # Startup is not a request: a short ATTEST_ENGINE_TIMEOUT must not
            # stop the replacement engine that follows a timeout from starting.
            timeout=max(_engine_timeout(), _DEFAULT_ENGINE_TIMEOUT),
        )
        init = InitializeResult.from_dict(result)
        if init.engine_version != ENGINE_VERSION:
            raise EngineVersionMismatchError(
                self._engine_path, "initialize", init.engine_version, ENGINE_VERSION
            )
        if not init.compatible:
            raise RuntimeError(f"Engine incompatible. Missing capabilities: {init.missing}")
        return init

    async def stop(self) -> None:
        """Send shutdown and wait for process exit.

        An attached ``AttestClient`` releases stdout first: the shutdown reply is read here.
        """
        if self._process is None:
            return
        if self._client is not None:
            await self._client.release_engine()
        if self._initialized:
            try:
                await self._handshake("shutdown", {})
            except Exception:
                logger.warning("Shutdown request failed, killing process")
        await self._kill()

    async def _kill(self) -> None:
        if self._process is None:
            return
        if self._process.returncode is None:
            self._process.terminate()
            try:
                await asyncio.wait_for(self._process.wait(), timeout=5.0)
            except asyncio.TimeoutError:
                self._process.kill()
                await self._process.wait()
        self._initialized = False
        logger.info("Engine stopped")

    async def _handshake(
        self, method: str, params: dict[str, Any], timeout: float | None = None
    ) -> Any:
        """Send one request and read until the response carrying its id.

        Lines without an id and lines for other ids are logged and dropped;
        none is ever taken as this request's response. Only valid while no
        ``AttestClient`` reader owns stdout (``initialize`` and ``shutdown``).
        """
        assert self._process is not None
        assert self._process.stdin is not None
        assert self._process.stdout is not None

        self._request_id += 1
        request_id = self._request_id
        self._process.stdin.write(encode_request(request_id, method, params))
        await self._process.stdin.drain()

        timeout = _engine_timeout() if timeout is None else timeout
        deadline = time.monotonic() + timeout
        while True:
            remaining = deadline - time.monotonic()
            try:
                if remaining <= 0:
                    raise asyncio.TimeoutError
                line = await asyncio.wait_for(self._process.stdout.readline(), timeout=remaining)
            except asyncio.TimeoutError:
                raise EngineTimeoutError(method=method, timeout=timeout) from None
            if not line:
                raise ConnectionError("Engine process closed stdout unexpectedly")

            try:
                response = decode_response(line)
            except ProtocolError:
                # An engine error is this request's response only when its id matches.
                if _line_id(line) != request_id:
                    logger.warning("Dropped engine error line during %s: %.200r", method, line)
                    continue
                raise
            except ValueError:
                logger.warning("Dropped undecodable engine line during %s: %.200r", method, line)
                continue
            try:
                line_id = extract_id(response)
            except ValueError:
                logger.warning("Dropped engine line without id during %s: %.200r", method, line)
                continue
            if line_id != request_id:
                logger.warning(
                    "Dropped engine line for id=%s while waiting for id=%s (%s)",
                    line_id,
                    request_id,
                    method,
                )
                continue
            return extract_result(response)

    async def __aenter__(self) -> EngineManager:
        await self.start()
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.stop()

    @property
    def is_running(self) -> bool:
        return self._process is not None and self._process.returncode is None
