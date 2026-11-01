"""Engine subprocess lifecycle manager."""

from __future__ import annotations

import asyncio
import logging
import os
import shutil
import subprocess
from typing import Any

from attest import ENGINE_VERSION, __version__
from attest._proto.codec import decode_response, encode_request, extract_result
from attest._proto.types import InitializeParams, InitializeResult
from attest.engine_downloader import cached_engine_path, download_engine
from attest.exceptions import EngineTimeoutError, EngineVersionMismatchError

logger = logging.getLogger("attest.engine")

ENGINE_BINARY_NAME = "attest-engine"

_DEFAULT_ENGINE_TIMEOUT: float = 30.0

_VERSION_PROBE_TIMEOUT: float = 10.0


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


def _locate_engine(explicit_path: str | None) -> tuple[str, str]:
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

    found = shutil.which(ENGINE_BINARY_NAME)
    if found:
        return found, "PATH"

    cached = cached_engine_path()
    if cached is not None:
        return str(cached), "engine cache"

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


def resolve_engine(explicit_path: str | None = None) -> str:
    """Find an engine whose version matches this SDK and return its path.

    The single discovery function behind the pytest fixture, ``EngineManager`` and the
    ``attest`` CLI. Discovery order:

        1. ``explicit_path`` (``--attest-engine`` / ``EngineManager(engine_path=...)``)
        2. ``ATTEST_ENGINE_PATH``
        3. ``attest-engine`` on ``PATH``
        4. ``~/.attest/bin/<ENGINE_VERSION>/`` cache
        5. Download ``ENGINE_VERSION`` from GitHub Releases (unless
           ``ATTEST_ENGINE_NO_DOWNLOAD`` is set)

    The first engine found must report exactly ``ENGINE_VERSION``; discovery never skips a
    mismatched engine to try the next location.

    Raises:
        FileNotFoundError: No engine was found and download is disabled.
        EngineVersionMismatchError: The engine found reports a different version.
    """
    path, source = _locate_engine(explicit_path)
    found = engine_version(path)
    if found != ENGINE_VERSION:
        raise EngineVersionMismatchError(path, source, found, ENGINE_VERSION)
    return path


class EngineManager:
    """Manages the lifecycle of the attest-engine subprocess."""

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

    async def start(self) -> InitializeResult:
        """Start the engine subprocess and send initialize.

        Any failure after the process starts stops it before the error propagates.
        """
        self._process = await asyncio.create_subprocess_exec(
            self._engine_path,
            f"--log-level={self._log_level}",
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        logger.info("Engine started (pid=%d)", self._process.pid)
        try:
            self._init_result = await self._initialize()
        except BaseException:
            await self.stop()
            raise
        self._initialized = True
        return self._init_result

    async def _initialize(self) -> InitializeResult:
        """Send initialize and check the engine's version and capabilities."""
        result = await self._send_request(
            "initialize",
            InitializeParams(
                sdk_name="attest-python",
                sdk_version=__version__,
                protocol_version=1,
                required_capabilities=["layers_1_4"],
            ).to_dict(),
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
        """Send shutdown and wait for process exit."""
        if self._process is None:
            return
        if self._initialized:
            try:
                await self._send_request("shutdown", {})
            except Exception:
                logger.warning("Shutdown request failed, killing process")
        if self._process.returncode is None:
            self._process.terminate()
            try:
                await asyncio.wait_for(self._process.wait(), timeout=5.0)
            except asyncio.TimeoutError:
                self._process.kill()
                await self._process.wait()
        self._initialized = False
        logger.info("Engine stopped")

    async def send_request(self, method: str, params: dict[str, Any]) -> Any:
        """Send a JSON-RPC request and return the result."""
        if not self._initialized and method != "initialize":
            raise RuntimeError("Engine not initialized. Call start() first.")
        return await self._send_request(method, params)

    async def _send_request(self, method: str, params: dict[str, Any]) -> Any:
        """Internal: send request and read response."""
        assert self._process is not None
        assert self._process.stdin is not None
        assert self._process.stdout is not None

        self._request_id += 1
        request_bytes = encode_request(self._request_id, method, params)

        self._process.stdin.write(request_bytes)
        await self._process.stdin.drain()

        timeout = _engine_timeout()
        try:
            line = await asyncio.wait_for(self._process.stdout.readline(), timeout=timeout)
        except asyncio.TimeoutError:
            raise EngineTimeoutError(method=method, timeout=timeout)

        if not line:
            raise ConnectionError("Engine process closed stdout unexpectedly")

        response = decode_response(line)
        return extract_result(response)

    async def __aenter__(self) -> EngineManager:
        await self.start()
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.stop()

    @property
    def is_running(self) -> bool:
        return self._process is not None and self._process.returncode is None
