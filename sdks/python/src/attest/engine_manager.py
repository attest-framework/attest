"""Engine subprocess lifecycle manager."""

from __future__ import annotations

import asyncio
import logging
import os
import shutil
from typing import Any

from attest import __version__
from attest._proto.codec import decode_response, encode_request, extract_result
from attest._proto.types import InitializeParams, InitializeResult

logger = logging.getLogger("attest.engine")

ENGINE_BINARY_NAME = "attest-engine"


def _is_download_disabled() -> bool:
    """Check if auto-download is disabled via ATTEST_ENGINE_NO_DOWNLOAD."""
    val = os.environ.get("ATTEST_ENGINE_NO_DOWNLOAD", "").lower()
    return val in ("1", "true", "yes")


def _find_engine_binary() -> str:
    """Find the engine binary using the full discovery chain.

    Discovery order:
        1. ATTEST_ENGINE_PATH env var (absolute path override)
        2. PATH lookup (shutil.which)
        3. ~/.attest/bin/ shared cache (version-checked)
        4. Monorepo dev layout (../../bin/)
        5. Local ./bin/
        6. Auto-download from GitHub Releases
        7. FileNotFoundError with actionable message
    """
    # Step 1: Explicit env override
    env_path = os.environ.get("ATTEST_ENGINE_PATH")
    if env_path:
        if not os.path.isfile(env_path):
            raise FileNotFoundError(
                f"ATTEST_ENGINE_PATH={env_path} does not exist or is not a file."
            )
        return env_path

    # Step 2: PATH lookup
    found = shutil.which(ENGINE_BINARY_NAME)
    if found:
        return found

    # Step 3: Shared cache (~/.attest/bin/) with version check
    from attest.engine_downloader import cached_engine_path

    cached = cached_engine_path()
    if cached is not None:
        return str(cached)

    # Step 4: Monorepo dev layout
    # Step 5: Local ./bin/
    candidates = [
        os.path.join(os.path.dirname(__file__), "..", "..", "..", "bin", ENGINE_BINARY_NAME),
        os.path.join(os.getcwd(), "bin", ENGINE_BINARY_NAME),
    ]
    for candidate in candidates:
        resolved = os.path.realpath(candidate)
        if os.path.isfile(resolved) and os.access(resolved, os.X_OK):
            return resolved

    # Step 6: Auto-download
    if not _is_download_disabled():
        from attest.engine_downloader import download_engine

        downloaded = download_engine()
        return str(downloaded)

    # Step 7: Actionable error
    raise FileNotFoundError(
        f"Cannot find '{ENGINE_BINARY_NAME}' binary.\n"
        "Install options:\n"
        "  1. Build from source: make engine\n"
        "  2. Download a release: https://github.com/attest-framework/attest/releases\n"
        "  3. Allow auto-download by unsetting ATTEST_ENGINE_NO_DOWNLOAD\n"
        "  4. Set ATTEST_ENGINE_PATH to point to the binary"
    )


class EngineManager:
    """Manages the lifecycle of the attest-engine subprocess."""

    def __init__(
        self,
        engine_path: str | None = None,
        log_level: str = "warn",
    ) -> None:
        self._engine_path = engine_path or _find_engine_binary()
        self._log_level = log_level
        self._process: asyncio.subprocess.Process | None = None
        self._initialized = False
        self._request_id = 0
        self._init_result: InitializeResult | None = None

    async def start(self) -> InitializeResult:
        """Start the engine subprocess and send initialize."""
        self._process = await asyncio.create_subprocess_exec(
            self._engine_path,
            f"--log-level={self._log_level}",
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        logger.info("Engine started (pid=%d)", self._process.pid)

        result = await self._send_request("initialize", InitializeParams(
            sdk_name="attest-python",
            sdk_version=__version__,
            protocol_version=1,
            required_capabilities=["layers_1_4"],
        ).to_dict())

        self._init_result = InitializeResult.from_dict(result)
        if not self._init_result.compatible:
            raise RuntimeError(
                f"Engine incompatible. Missing capabilities: {self._init_result.missing}"
            )
        self._initialized = True
        return self._init_result

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

        line = await self._process.stdout.readline()
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
