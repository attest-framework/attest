"""Attest protocol client with request/response correlation."""

from __future__ import annotations

import asyncio
import logging
from typing import Any

from attest._proto.codec import (
    ProtocolError,
    decode_response,
    encode_request,
    extract_id,
    extract_result,
)
from attest._proto.types import (
    Assertion,
    AssertionResult,
    EvaluateBatchResult,
    Trace,
)
from attest.engine_manager import EngineManager

logger = logging.getLogger("attest.client")


class AttestClient:
    """High-level client for communicating with the attest engine.

    Owns request ID generation and asyncio-future-based response correlation.
    Concurrent callers each get an independent Future; the reader loop routes
    responses to the correct caller by ID.

    The underlying engine uses NDJSON over stdin/stdout (sequential protocol),
    so requests are serialized through a write lock while reads are dispatched
    by the shared reader loop.
    """

    def __init__(self, engine: EngineManager) -> None:
        self._engine = engine
        self._request_id: int = 0
        self._pending: dict[int, asyncio.Future[Any]] = {}
        self._write_lock = asyncio.Lock()
        self._reader_task: asyncio.Task[None] | None = None

    # ── Lifecycle ──

    def start_reader(self) -> None:
        """Start the background reader loop. Call after engine.start()."""
        if self._reader_task is None or self._reader_task.done():
            self._reader_task = asyncio.get_running_loop().create_task(
                self._reader_loop(), name="attest-client-reader"
            )

    async def stop_reader(self) -> None:
        """Cancel and await the reader loop."""
        if self._reader_task is not None and not self._reader_task.done():
            self._reader_task.cancel()
            try:
                await self._reader_task
            except asyncio.CancelledError:
                pass
        self._reader_task = None

    async def _reader_loop(self) -> None:
        """Read responses from the engine and resolve pending futures by ID."""
        process = self._engine._process
        if process is None or process.stdout is None:
            raise RuntimeError("Engine process not started.")

        while True:
            try:
                line = await process.stdout.readline()
            except Exception as exc:
                self._fail_all(exc)
                return

            if not line:
                self._fail_all(ConnectionError("Engine closed stdout."))
                return

            try:
                response = decode_response(line)
            except ProtocolError as exc:
                # Route error to the specific request by extracting raw id
                import json as _json
                try:
                    raw: Any = _json.loads(line.strip())
                    req_id = int(raw.get("id", -1))
                except Exception:
                    req_id = -1
                fut = self._pending.pop(req_id, None)
                if fut is not None and not fut.done():
                    fut.set_exception(exc)
                continue
            except ValueError as exc:
                logger.warning("Malformed response line: %s", exc)
                continue

            try:
                req_id = extract_id(response)
            except ValueError:
                logger.warning("Response missing id field, discarding")
                continue

            fut = self._pending.pop(req_id, None)
            if fut is None:
                logger.warning("No pending request for id=%d, discarding", req_id)
                continue

            if not fut.done():
                try:
                    result = extract_result(response)
                    fut.set_result(result)
                except Exception as exc:
                    fut.set_exception(exc)

    def _fail_all(self, exc: BaseException) -> None:
        """Fail all pending futures with the given exception."""
        for fut in self._pending.values():
            if not fut.done():
                fut.set_exception(exc)
        self._pending.clear()

    # ── Core send ──

    async def send_request(self, method: str, params: dict[str, Any]) -> Any:
        """Send a JSON-RPC request and return the correlated result.

        Assigns an auto-incrementing request ID, registers a Future in the
        pending map, writes the encoded request under the write lock, then
        awaits the Future which the reader loop resolves when the matching
        response arrives.

        Falls back to EngineManager.send_request when the reader loop is not
        running (e.g. during engine initialization before start_reader()).
        """
        if self._reader_task is None or self._reader_task.done():
            # Reader not running — delegate to engine directly (sequential mode)
            return await self._engine.send_request(method, params)

        loop = asyncio.get_running_loop()
        fut: asyncio.Future[Any] = loop.create_future()

        async with self._write_lock:
            self._request_id += 1
            req_id = self._request_id
            self._pending[req_id] = fut

            process = self._engine._process
            if process is None or process.stdin is None:
                self._pending.pop(req_id, None)
                fut.cancel()
                raise RuntimeError("Engine process not started.")

            request_bytes = encode_request(req_id, method, params)
            process.stdin.write(request_bytes)
            await process.stdin.drain()

        return await fut

    # ── Convenience methods ──

    async def evaluate_batch(
        self,
        trace: Trace,
        assertions: list[Assertion],
    ) -> EvaluateBatchResult:
        """Send evaluate_batch request and return parsed results.

        In simulation mode, returns deterministic pass results without
        spawning the engine or making API calls.
        """
        from attest.config import is_simulation_mode

        if is_simulation_mode():
            return _simulation_evaluate_batch(assertions)

        params: dict[str, Any] = {
            "trace": trace.to_dict(),
            "assertions": [a.to_dict() for a in assertions],
        }
        raw = await self.send_request("evaluate_batch", params)
        return EvaluateBatchResult.from_dict(raw)

    async def submit_plugin_result(
        self,
        trace_id: str,
        plugin_name: str,
        assertion_id: str,
        status: str,
        score: float,
        explanation: str,
    ) -> bool:
        """Submit a plugin-computed result to the engine."""
        params: dict[str, Any] = {
            "trace_id": trace_id,
            "plugin_name": plugin_name,
            "assertion_id": assertion_id,
            "result": {
                "assertion_id": assertion_id,
                "status": status,
                "score": score,
                "explanation": explanation,
                "cost": 0.0,
                "duration_ms": 0,
            },
        }
        raw = await self.send_request("submit_plugin_result", params)
        return bool(raw.get("accepted", False))


def _simulation_evaluate_batch(assertions: list[Assertion]) -> EvaluateBatchResult:
    """Return deterministic pass results for all assertions without engine."""
    results = [
        AssertionResult(
            assertion_id=a.assertion_id,
            status="pass",
            score=1.0,
            explanation=f"[simulation] {a.type} assertion passed (deterministic)",
            cost=0.0,
            duration_ms=0,
            request_id=a.request_id,
        )
        for a in assertions
    ]
    return EvaluateBatchResult(
        results=results,
        total_cost=0.0,
        total_duration_ms=0,
    )
