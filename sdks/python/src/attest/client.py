"""Attest protocol client with request/response correlation."""

from __future__ import annotations

import asyncio
import json as _json
import logging
import time
from collections.abc import Callable
from typing import Any

from attest._proto.codec import (
    ProtocolError,
    decode_response,
    encode_request,
    extract_id,
    extract_result,
)
from attest._proto.diagnostics import (
    DEFAULT_BUFFER_SIZE,
    DEFAULT_DESYNC_THRESHOLD,
    DEFAULT_DESYNC_WINDOW_SECONDS,
    ProtocolDiagnostic,
    ProtocolDiagnosticBuffer,
    ProtocolDiagnosticKind,
    ProtocolLogger,
    preview_line,
)
from attest._proto.types import (
    Assertion,
    AssertionResult,
    ConversationMessage,
    DriftReport,
    EvaluateBatchResult,
    SimulateFaultConfig,
    SimulatePersona,
    Trace,
)
from attest.engine_manager import EngineManager
from attest.exceptions import ProtocolDesyncError

logger = logging.getLogger("attest.client")


def _env_debug_enabled() -> bool:
    import os

    flag = os.environ.get("ATTEST_DEBUG_PROTOCOL", "")
    return flag in {"1", "true", "yes"}


_silent_logger = logging.getLogger("attest.client.protocol.silent")
_silent_logger.addHandler(logging.NullHandler())
_silent_logger.propagate = False


def _default_logger() -> ProtocolLogger:
    if _env_debug_enabled():
        return logger
    return _silent_logger


def _classify_decode_error(message: str) -> ProtocolDiagnosticKind:
    if message.startswith("malformed JSON"):
        return "malformed_json"
    if message.startswith("expected JSON object"):
        return "non_object_response"
    if message.startswith("invalid jsonrpc version"):
        return "invalid_jsonrpc_version"
    if message.startswith("empty response line"):
        return "malformed_json"
    return "malformed_json"


class AttestClient:
    """High-level client for communicating with the attest engine.

    Owns request ID generation and asyncio-future-based response correlation.
    Concurrent callers each get an independent Future; the reader loop routes
    responses to the correct caller by ID.

    The underlying engine uses NDJSON over stdin/stdout (sequential protocol),
    so requests are serialized through a write lock while reads are dispatched
    by the shared reader loop.

    Desync detection is a one-way latch: once the diagnostic rate breaches
    ``desync_threshold`` within ``desync_window_seconds`` the client refuses
    further sends for the lifetime of the instance. Recovery requires
    constructing a new client over a fresh engine subprocess.

    Args:
        engine: The :class:`EngineManager` controlling the engine subprocess.
        protocol_logger: Logger used for protocol diagnostics. Defaults to a
            silent logger unless ``ATTEST_DEBUG_PROTOCOL=1``.
        on_diagnostic: Callback invoked for every protocol diagnostic.
        diagnostic_buffer_size: Capacity of the diagnostic ring buffer.
        desync_threshold: Number of diagnostics in
            ``desync_window_seconds`` that triggers a desync rejection.
        desync_window_seconds: Sliding window length used by the desync
            detector.
    """

    def __init__(
        self,
        engine: EngineManager,
        *,
        protocol_logger: ProtocolLogger | None = None,
        on_diagnostic: Callable[[ProtocolDiagnostic], None] | None = None,
        diagnostic_buffer_size: int = DEFAULT_BUFFER_SIZE,
        desync_threshold: int = DEFAULT_DESYNC_THRESHOLD,
        desync_window_seconds: float = DEFAULT_DESYNC_WINDOW_SECONDS,
    ) -> None:
        self._engine = engine
        self._request_id: int = 0
        self._pending: dict[int, asyncio.Future[Any]] = {}
        self._write_lock = asyncio.Lock()
        self._reader_task: asyncio.Task[None] | None = None

        self._logger: ProtocolLogger = protocol_logger or _default_logger()
        self._diagnostic_buffer = ProtocolDiagnosticBuffer(diagnostic_buffer_size)
        self._diagnostic_listeners: list[Callable[[ProtocolDiagnostic], None]] = []
        if on_diagnostic is not None:
            self._diagnostic_listeners.append(on_diagnostic)
        self._desync_threshold = desync_threshold
        self._desync_window_seconds = desync_window_seconds
        self._desynced = False

    # ── Diagnostics API ──

    def on_protocol_diagnostic(
        self,
        listener: Callable[[ProtocolDiagnostic], None],
    ) -> Callable[[], None]:
        """Subscribe to protocol diagnostics. Returns an unsubscribe callable."""
        self._diagnostic_listeners.append(listener)

        def _unsubscribe() -> None:
            try:
                self._diagnostic_listeners.remove(listener)
            except ValueError:
                pass

        return _unsubscribe

    def protocol_diagnostics(self) -> tuple[ProtocolDiagnostic, ...]:
        """Snapshot the diagnostic ring buffer (most recent last)."""
        return self._diagnostic_buffer.snapshot()

    def _record_diagnostic(
        self,
        kind: ProtocolDiagnosticKind,
        message: str,
        raw_line: str,
    ) -> None:
        diagnostic = ProtocolDiagnostic(
            kind=kind,
            message=message,
            raw_line=preview_line(raw_line),
            timestamp=time.monotonic(),
        )
        self._diagnostic_buffer.push(diagnostic)
        self._logger.warning("[attest.protocol] %s: %s", kind, message)
        for cb in list(self._diagnostic_listeners):
            try:
                cb(diagnostic)
            except Exception as exc:  # noqa: BLE001 - listener bugs must not crash reader
                self._logger.error("[attest.protocol] diagnostic listener threw: %s", exc)
        self._maybe_trigger_desync(diagnostic.timestamp)

    def _maybe_trigger_desync(self, now: float) -> None:
        if self._desynced:
            return
        recent = self._diagnostic_buffer.count_within(self._desync_window_seconds, now)
        if recent >= self._desync_threshold:
            self._desynced = True
            snapshot = self._diagnostic_buffer.snapshot()
            err = ProtocolDesyncError(
                f"Engine protocol desync: {recent} unroutable response lines "
                f"within {self._desync_window_seconds}s.",
                snapshot,
            )
            self._logger.error("[attest.protocol] %s", err)
            self._fail_all(err)

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
        """Read responses from the engine and route them to pending callers."""
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

            self._handle_line(line)

    def _handle_line(self, line: bytes) -> None:
        text = line.decode("utf-8", errors="replace")

        try:
            response = decode_response(line)
        except ProtocolError as exc:
            self._handle_protocol_error(exc, text)
            return
        except ValueError as exc:
            kind = _classify_decode_error(str(exc))
            self._record_diagnostic(kind, str(exc), text)
            return

        try:
            req_id = extract_id(response)
        except ValueError as exc:
            self._record_diagnostic("missing_id", str(exc), text)
            return

        fut = self._pending.pop(req_id, None)
        if fut is None:
            self._record_diagnostic("unknown_id", f"no pending request for id={req_id}", text)
            return

        if fut.done():
            return

        try:
            result = extract_result(response)
            fut.set_result(result)
        except Exception as exc:
            fut.set_exception(exc)

    def _handle_protocol_error(self, exc: ProtocolError, text: str) -> None:
        try:
            raw: Any = _json.loads(text.strip())
            id_value = raw.get("id")
            req_id = (
                int(id_value)
                if isinstance(id_value, int) or (isinstance(id_value, str) and id_value.isdigit())
                else -1
            )
        except (ValueError, TypeError, AttributeError):
            req_id = -1

        fut = self._pending.pop(req_id, None)
        if fut is not None and not fut.done():
            fut.set_exception(exc)
            return

        self._record_diagnostic(
            "non_routable_error",
            f"engine error code={exc.code} ({exc.error_message}) had no matching pending id",
            text,
        )

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
        if self._desynced:
            raise ProtocolDesyncError(
                "Engine protocol is desynced; AttestClient will not send further requests.",
                self._diagnostic_buffer.snapshot(),
            )

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

    async def query_drift(
        self,
        assertion_id: str,
        window_size: int = 50,
    ) -> DriftReport:
        """Query drift status for an assertion against historical scores.

        Args:
            assertion_id: The assertion to check drift for.
            window_size: Number of recent scores to include (default 50).

        Returns:
            DriftReport with mean, stddev, deviation, and status.
        """
        from attest.config import is_simulation_mode

        if is_simulation_mode():
            return DriftReport(
                assertion_id=assertion_id,
                mean=1.0,
                stddev=0.0,
                count=0,
                latest_score=1.0,
                deviation=0.0,
                status="no_data",
            )

        params: dict[str, Any] = {
            "assertion_id": assertion_id,
            "window_size": window_size,
        }
        raw = await self.send_request("query_drift", params)
        return DriftReport.from_dict(raw["report"])

    async def generate_user_message(
        self,
        persona: SimulatePersona,
        conversation_history: list[ConversationMessage],
        fault_config: SimulateFaultConfig | None = None,
    ) -> str:
        """Generate a simulated user message using the engine's LLM provider.

        Args:
            persona: Persona configuration (name, style, temperature).
            conversation_history: Prior conversation messages.
            fault_config: Optional fault injection settings.

        Returns:
            Generated message text.
        """
        from attest.config import is_simulation_mode

        if is_simulation_mode():
            return f"[simulation] Hello from {persona.name}"

        params: dict[str, Any] = {
            "persona": persona.to_dict(),
            "conversation_history": [m.to_dict() for m in conversation_history],
        }
        if fault_config is not None:
            params["fault_config"] = fault_config.to_dict()
        raw = await self.send_request("generate_user_message", params)
        return str(raw["message"])


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
        simulated=True,
    )
