"""Attest protocol client with request/response correlation."""

from __future__ import annotations

import asyncio
import json as _json
import logging
import os
import time
from collections.abc import Callable
from dataclasses import dataclass
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
    TYPE_EMBEDDING,
    TYPE_LLM_JUDGE,
    Assertion,
    BatchBudget,
    BatchContext,
    ConversationMessage,
    DriftReport,
    EvaluateBatchResult,
    InitializeResult,
    SimulateFaultConfig,
    SimulatePersona,
    Trace,
    Usage,
)
from attest.engine_manager import EngineManager, _engine_timeout
from attest.exceptions import EngineTimeoutError, ProtocolDesyncError

logger = logging.getLogger("attest.client")


def _env_debug_enabled() -> bool:
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
        # request id -> budget exposure of requests that have been sent and not answered.
        self._inflight: dict[int, _Exposure] = {}
        # ids whose caller gave up; a late response for one is dropped quietly.
        self._abandoned: set[int] = set()
        self._restart_lock = asyncio.Lock()
        self._generation = 0
        # Usage returned by evaluate_batch, summed over every engine of the session.
        self._spent = Usage()
        # Limits as configured when the first engine started. Later engines run with a
        # reduced copy of them, so the restart arithmetic never reads engine.spawn_env again.
        self._limits_captured = False
        self._limit_calls: int | None = None
        self._limit_usd: float | None = None
        # Per-batch caps of requests lost in a restart: that much of the session budget
        # may have been spent without the SDK seeing the usage.
        self._held_calls = 0
        self._held_usd = 0.0
        # Spend the SDK cannot observe (a lost request without a per-batch cap, a reply
        # without usage, an error reply) may have used all the remaining budget of its
        # engine; the limit is then exhausted for every later engine.
        self._judge_calls_exhausted = False
        self._usd_exhausted = False

        self._logger: ProtocolLogger = protocol_logger or _default_logger()
        self._diagnostic_buffer = ProtocolDiagnosticBuffer(diagnostic_buffer_size)
        self._diagnostic_listeners: list[Callable[[ProtocolDiagnostic], None]] = []
        if on_diagnostic is not None:
            self._diagnostic_listeners.append(on_diagnostic)
        self._desync_threshold = desync_threshold
        self._desync_window_seconds = desync_window_seconds
        self._desynced = False
        engine.attach_client(self)

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

    async def release_engine(self) -> None:
        """Hand stdout back to the engine manager for its shutdown handshake.

        Stops the reader and fails every pending request. ``EngineManager.stop``
        calls this; the reader is blocked in ``readline`` and would otherwise
        collide with the manager's own read of the shutdown reply.
        """
        await self.stop_reader()
        self._fail_all(ConnectionError("Engine is shutting down; this request was lost."))

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

        if "id" not in response and "method" in response:
            logger.info("Dropped engine notification %r (not a response)", response["method"])
            return

        try:
            req_id = extract_id(response)
        except ValueError as exc:
            self._record_diagnostic("missing_id", str(exc), text)
            return

        fut = self._pending.pop(req_id, None)
        exposure = self._inflight.pop(req_id, None)
        if exposure is not None and not exposure.reports_usage:
            self._exhaust(exposure)  # a reply without usage: the spend is unknown
        if fut is None:
            if req_id in self._abandoned:
                self._abandoned.discard(req_id)
                if exposure is not None:
                    self._exhaust(exposure)  # the dropped reply carried usage nobody read
                logger.info("Dropped late response for abandoned request id=%d", req_id)
                return
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
        exposure = self._inflight.pop(req_id, None)
        if exposure is not None:
            # The engine may have called a provider before it failed the request.
            self._exhaust(exposure)
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
        self._inflight.clear()

    # ── Core send ──

    async def send_request(self, method: str, params: dict[str, Any]) -> Any:
        """Send a JSON-RPC request and return the result of its own response.

        Assigns an auto-incrementing request ID, registers a Future in the
        pending map, writes the encoded request under the write lock, then
        awaits the Future which the reader loop resolves when the response
        with the same ID arrives. The reader starts on first use.

        After ``ATTEST_ENGINE_TIMEOUT`` seconds without that response the
        engine is killed and restarted (its pipe may still hold the answer),
        every other pending request fails, and ``EngineTimeoutError`` is
        raised. The new engine gets the session budget that remains.
        """
        if self._desynced:
            raise ProtocolDesyncError(
                "Engine protocol is desynced; AttestClient will not send further requests.",
                self._diagnostic_buffer.snapshot(),
            )

        loop = asyncio.get_running_loop()
        fut: asyncio.Future[Any] = loop.create_future()

        while True:
            async with self._restart_lock:  # wait out a restart in progress
                self._ensure_reader()
            seen = self._generation
            async with self._write_lock:
                if self._restart_lock.locked() or seen != self._generation:
                    continue  # a restart began while queued: write to the new engine instead
                self._request_id += 1
                req_id = self._request_id
                generation = self._generation

                process = self._engine._process
                if process is None or process.stdin is None:
                    raise RuntimeError("Engine process not started.")

                self._pending[req_id] = fut
                self._inflight[req_id] = _budget_exposure(method, params)
                request_bytes = encode_request(req_id, method, params)
                try:
                    process.stdin.write(request_bytes)
                    await process.stdin.drain()
                except BaseException:
                    self._forget(req_id)
                    raise
                break

        timeout = _engine_timeout()
        try:
            done, _ = await asyncio.wait({fut}, timeout=timeout)
        except asyncio.CancelledError:
            self._abandon(req_id)
            raise
        if done:
            return fut.result()

        self._abandon(req_id)
        await self._restart_after_timeout(generation, method, timeout)
        raise EngineTimeoutError(method=method, timeout=timeout)

    def _ensure_reader(self) -> None:
        self._capture_limits()
        if self._reader_task is None:
            self.start_reader()
            return
        if self._reader_task.get_loop() is not asyncio.get_running_loop():
            raise RuntimeError(
                "AttestClient is bound to the event loop that first used it; "
                "submit requests from that loop."
            )
        if self._reader_task.done():
            raise ConnectionError("Engine reader stopped; the engine closed stdout or failed.")

    def _forget(self, req_id: int) -> None:
        self._pending.pop(req_id, None)
        self._inflight.pop(req_id, None)

    def _abandon(self, req_id: int) -> None:
        """Stop waiting for ``req_id``; a late response for it is dropped."""
        if self._pending.pop(req_id, None) is not None:
            self._abandoned.add(req_id)

    async def _restart_after_timeout(self, generation: int, method: str, timeout: float) -> None:
        """Replace the engine after a timeout, once per engine generation."""
        async with self._restart_lock:
            if generation != self._generation:
                return  # another timed-out request already restarted the engine
            self._generation += 1
            for exposure in self._inflight.values():
                self._hold(exposure)
            await self.stop_reader()
            self._fail_all(
                ConnectionError(
                    f"Engine restarted after a {method} request timed out after {timeout:g}s; "
                    "this request was lost with it."
                )
            )
            self._abandoned.clear()
            logger.warning("%s timed out after %gs; restarting the engine", method, timeout)
            await self._engine.restart(self._remaining_budget_env())
            self.start_reader()

    def _exhaust(self, exposure: _Exposure) -> None:
        """Treat the budget ``exposure`` can spend as unknown: nothing of it remains."""
        self._judge_calls_exhausted |= exposure.judge
        self._usd_exhausted |= exposure.usd

    def _hold(self, exposure: _Exposure) -> None:
        """Reserve what a lost request may have spent: its per-batch cap, else everything."""
        if exposure.judge:
            if exposure.max_calls is None:
                self._judge_calls_exhausted = True
            else:
                self._held_calls += exposure.max_calls
        if exposure.usd:
            if exposure.max_usd is None:
                self._usd_exhausted = True
            else:
                self._held_usd += exposure.max_usd

    def _capture_limits(self) -> None:
        """Record the configured session limits once, from the first engine's environment."""
        if self._limits_captured:
            return
        env = self._engine.spawn_env
        raw_calls = env.get("ATTEST_MAX_JUDGE_CALLS", "")
        raw_usd = env.get("ATTEST_BUDGET_USD", "")
        self._limit_calls = int(raw_calls) if raw_calls else None
        self._limit_usd = float(raw_usd) if raw_usd else None
        self._limits_captured = True

    def _remaining_budget_env(self) -> dict[str, str]:
        """Budget variables for the replacement engine (contract C1).

        The session limit minus the usage evaluate_batch already returned,
        minus the worst-case reservation of requests lost in the restart. The
        engine ledger admits at most the remaining limit, so a lost request
        that could reach a model provider may have used all of it: that limit
        is exhausted (0) and the new engine answers ``budget_exhausted``.
        """
        self._capture_limits()
        out: dict[str, str] = {}
        if self._limit_calls is not None:
            left = (
                0
                if self._judge_calls_exhausted
                else self._limit_calls - self._spent.judge_calls - self._held_calls
            )
            out["ATTEST_MAX_JUDGE_CALLS"] = str(max(0, left))
        if self._limit_usd is not None:
            left_usd = (
                0.0
                if self._usd_exhausted
                else self._limit_usd - self._spent.cost_usd - self._held_usd
            )
            out["ATTEST_BUDGET_USD"] = repr(max(0.0, left_usd))
        return out

    # ── Convenience methods ──

    async def evaluate_batch(
        self,
        trace: Trace,
        assertions: list[Assertion],
        *,
        budget: BatchBudget | None = None,
        context: BatchContext | None = None,
    ) -> EvaluateBatchResult:
        """Send evaluate_batch request and return parsed results.

        ``budget`` limits this batch on top of the session budget (the pytest
        plugin sends the remainder of a per-test ``attest_budget`` marker);
        ``None`` omits the param. ``context`` names the running test so an engine
        recording with ``ATTEST_CONTRACTS_DIR`` can label each spec; ``None``
        omits the param.

        Simulation mode is handled by the engine: started with
        ``ATTEST_SIMULATION`` set, it evaluates layers 1-4 for real and
        returns ``not_evaluated`` (reason ``simulation``) for layers 5-6.

        Raises:
            RuntimeError: simulation mode is on but the running engine did not
                advertise ``simulation_mode``, so it would call real providers.
        """
        from attest.config import is_simulation_mode

        unidentified = [a.type for a in assertions if not a.assertion_id]
        if unidentified:
            raise ValueError(
                f"assertions without an assertion_id ({', '.join(unidentified)}): ids are "
                "assigned when an expect() chain is evaluated through the attest fixture"
            )
        if is_simulation_mode():
            # EngineManager keeps the initialize result privately; read it
            # here rather than trusting this process's environment.
            require_simulation_capability(self._engine._init_result)
        params: dict[str, Any] = {
            "trace": trace.to_dict(),
            "assertions": [a.to_dict() for a in assertions],
        }
        if budget is not None:
            params["budget"] = budget.to_dict()
        if context is not None:
            params["context"] = context.to_dict()
        raw = await self.send_request("evaluate_batch", params)
        result = EvaluateBatchResult.from_dict(raw)
        self._spent = self._spent + result.usage
        sent = {a.assertion_id for a in assertions}
        returned = {r.assertion_id for r in result.results}
        if sent != returned:
            raise ProtocolDesyncError(
                "evaluate_batch response does not belong to this request: sent assertion ids "
                f"{sorted(sent)[:5]} (n={len(sent)}), "
                f"got {sorted(returned)[:5]} (n={len(returned)}).",
                self._diagnostic_buffer.snapshot(),
            )
        return result

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
        static_threshold: float | None = None,
    ) -> DriftReport:
        """Query drift status for an assertion against historical scores.

        Args:
            assertion_id: The assertion to check drift for.
            window_size: Accepted for compatibility and ignored by the engine since 1.1,
                which fixes the baseline window.
            static_threshold: The assertion's static pass threshold in [0, 1], the floor the
                dynamic bar never falls below. Omitted from the request when None, so the
                engine uses its default of 0.8.

        Returns:
            DriftReport with mean, stddev, deviation, and status.
        """
        from attest.config import is_simulation_mode

        if is_simulation_mode():
            return DriftReport(
                assertion_id=assertion_id,
                mean=0.0,
                stddev=0.0,
                count=0,
                latest_score=0.0,
                deviation=0.0,
                status="no_data",
            )

        params: dict[str, Any] = {
            "assertion_id": assertion_id,
            "window_size": window_size,
        }
        if static_threshold is not None:
            params["static_threshold"] = static_threshold
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


@dataclass(frozen=True)
class _Exposure:
    """What one request can spend of the session budget."""

    judge: bool = False  # may call the judge provider
    usd: bool = False  # may spend USD
    reports_usage: bool = False  # its reply returns ``usage``
    max_calls: int | None = None  # per-batch ``budget.max_judge_calls``
    max_usd: float | None = None  # per-batch ``budget.max_cost_usd``


def _budget_exposure(method: str, params: dict[str, Any]) -> _Exposure:
    """The session budget a request may spend, with the caps it carries."""
    if method == "generate_user_message":
        return _Exposure(judge=True, usd=True)
    if method != "evaluate_batch":
        return _Exposure()
    types = {a.get("type") for a in params.get("assertions", [])}
    budget = params.get("budget") or {}
    return _Exposure(
        judge=TYPE_LLM_JUDGE in types,
        usd=bool(types & {TYPE_LLM_JUDGE, TYPE_EMBEDDING}),
        reports_usage=True,
        max_calls=budget.get("max_judge_calls"),
        max_usd=budget.get("max_cost_usd"),
    )


def require_simulation_capability(init: InitializeResult | None) -> None:
    """Raise unless the engine advertised ``simulation_mode`` at initialize.

    An engine started without ``ATTEST_SIMULATION`` (or older than 1.0)
    would call real embedding and judge providers in simulation mode.
    """
    if init is None:
        raise RuntimeError("simulation mode: the engine has not been initialized")
    if "simulation_mode" not in init.capabilities:
        raise RuntimeError(
            f"simulation mode is on but attest-engine {init.engine_version} was not started "
            "in simulation mode (capability 'simulation_mode' missing), so it would call real "
            "providers. Set ATTEST_SIMULATION=1 before the engine starts (the pytest plugin does "
            "this for you) and use attest-engine 1.0 or later."
        )
