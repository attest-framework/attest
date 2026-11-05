import type {
  Assertion,
  BatchBudget,
  BatchContext,
  ConversationMessage,
  DriftReport,
  EvaluateBatchParams,
  EvaluateBatchResult,
  InitializeResult,
  SimulateFaultConfig,
  SimulatePersona,
  Trace,
  AssertionResult,
} from "./proto/types.js";
import {
  ProtocolError,
  EngineTimeoutError,
  ProtocolDesyncError,
} from "./proto/errors.js";
import {
  decodeResponse,
  encodeRequest,
  extractId,
  extractResult,
} from "./proto/codec.js";
import {
  ProtocolDiagnosticBuffer,
  previewLine,
  type ProtocolDiagnostic,
  type ProtocolDiagnosticKind,
  type ProtocolLogger,
} from "./proto/diagnostics.js";
import type { Interface as ReadlineInterface } from "node:readline";
import type { EngineManager } from "./engine-manager.js";
import { isSimulationMode } from "./config.js";
import { parseUsage } from "./usage.js";

/** Budget a timed-out request may have consumed; null means the whole remaining budget. */
export interface BudgetReservation {
  readonly judgeCalls: number | null;
  readonly usd: number | null;
}

const DEFAULT_TIMEOUT_MS = 30_000;
const DEFAULT_DIAGNOSTIC_BUFFER = 32;
const DEFAULT_DESYNC_THRESHOLD = 3;
const DEFAULT_DESYNC_WINDOW_MS = 1_000;

const MAX_TIMEOUT_ENV_SECONDS = 3600;

/** Per-request timeout in milliseconds; ATTEST_ENGINE_TIMEOUT is read in seconds. */
export function resolveTimeoutMs(override?: number): number {
  if (override !== undefined) return override;
  const env = process.env["ATTEST_ENGINE_TIMEOUT"];
  if (env !== undefined && env !== "") {
    const parsed = Number(env);
    if (!Number.isFinite(parsed) || parsed <= 0) {
      throw new Error(
        `ATTEST_ENGINE_TIMEOUT must be a positive number of seconds, got: '${env}'`,
      );
    }
    if (parsed > MAX_TIMEOUT_ENV_SECONDS) {
      throw new Error(
        `ATTEST_ENGINE_TIMEOUT is in seconds since 1.1.0; got '${env}', which exceeds ${MAX_TIMEOUT_ENV_SECONDS}. ` +
          `A millisecond value such as 30000 must now be 30.`,
      );
    }
    return parsed * 1000;
  }
  return DEFAULT_TIMEOUT_MS;
}

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
  reservation: BudgetReservation;
}

export interface AttestClientOptions {
  /** Logger override. When omitted, warnings only emit if ATTEST_DEBUG_PROTOCOL=1. */
  logger?: ProtocolLogger;
  /** Listener invoked for every protocol diagnostic. */
  onDiagnostic?: (diagnostic: ProtocolDiagnostic) => void;
  /** Diagnostic ring-buffer capacity. Default 32. */
  diagnosticBufferSize?: number;
  /** Number of diagnostics in `desyncWindowMs` that triggers desync rejection. Default 3. */
  desyncThreshold?: number;
  /** Window length for desync detection in milliseconds. Default 1000. */
  desyncWindowMs?: number;
}

function debugProtocolEnabled(): boolean {
  const flag = process.env["ATTEST_DEBUG_PROTOCOL"];
  return flag === "1" || flag === "true" || flag === "yes";
}

function envLoggerDefault(): ProtocolLogger {
  if (debugProtocolEnabled()) {
    return {
      warn: (msg: string, ...args: unknown[]) => console.warn(msg, ...args),
      error: (msg: string, ...args: unknown[]) => console.error(msg, ...args),
    };
  }
  return {
    warn: () => {
      /* silent unless ATTEST_DEBUG_PROTOCOL=1 */
    },
    error: () => {
      /* silent unless ATTEST_DEBUG_PROTOCOL=1 */
    },
  };
}

/**
 * AttestClient owns request/response correlation over the engine's
 * NDJSON protocol.
 *
 * Desync detection is a one-way latch: once the diagnostic rate breaches
 * `desyncThreshold` within `desyncWindowMs` the client refuses further
 * sends for the lifetime of the instance. There is no reset hook by
 * design — recovery requires constructing a new client over a fresh
 * engine subprocess.
 */
export class AttestClient {
  private readonly engine: EngineManager;
  private requestId = 0;
  private readonly pending = new Map<number, PendingRequest>();
  private writeChain: Promise<void> = Promise.resolve();
  private readerActive = false;
  private lineHandler: ((line: string) => void) | null = null;
  private attachedReader: ReadlineInterface | null = null;

  private readonly logger: ProtocolLogger;
  private readonly diagnosticBuffer: ProtocolDiagnosticBuffer;
  private readonly diagnosticListeners = new Set<
    (diagnostic: ProtocolDiagnostic) => void
  >();
  private readonly desyncThreshold: number;
  private readonly desyncWindowMs: number;
  private desynced = false;

  constructor(engine: EngineManager, options?: AttestClientOptions) {
    this.engine = engine;
    this.logger = options?.logger ?? envLoggerDefault();
    this.diagnosticBuffer = new ProtocolDiagnosticBuffer(
      options?.diagnosticBufferSize ?? DEFAULT_DIAGNOSTIC_BUFFER,
    );
    this.desyncThreshold = options?.desyncThreshold ?? DEFAULT_DESYNC_THRESHOLD;
    this.desyncWindowMs = options?.desyncWindowMs ?? DEFAULT_DESYNC_WINDOW_MS;
    if (options?.onDiagnostic !== undefined) {
      this.diagnosticListeners.add(options.onDiagnostic);
    }
  }

  /** Subscribe to protocol diagnostics. Returns an unsubscribe function. */
  onProtocolDiagnostic(
    listener: (diagnostic: ProtocolDiagnostic) => void,
  ): () => void {
    this.diagnosticListeners.add(listener);
    return () => {
      this.diagnosticListeners.delete(listener);
    };
  }

  /** Snapshot of the diagnostic ring buffer (most recent last). */
  protocolDiagnostics(): readonly ProtocolDiagnostic[] {
    return this.diagnosticBuffer.snapshot();
  }

  /**
   * Attach the id-routed reader to the engine's stdout. Idempotent; `sendRequest` starts it
   * on first use, so calling it is optional.
   */
  startReader(): void {
    if (this.readerActive) return;

    const rl = this.engine.readlineInterface;
    if (rl === null) {
      throw new Error("Engine readline interface not available. Call engine.start() first.");
    }

    this.readerActive = true;
    this.attachedReader = rl;
    this.lineHandler = (line: string) => this.handleLine(line);
    rl.on("line", this.lineHandler);

    rl.on("close", () => {
      // A restarted engine has a new readline; the old one closing is not a failure.
      if (this.attachedReader !== rl) return;
      const tail = this.engine.stderrTail ?? "";
      this.failAll(
        new Error(tail === "" ? "Engine closed stdout." : `Engine closed stdout. stderr:\n${tail}`),
      );
      this.readerActive = false;
      this.attachedReader = null;
    });
  }

  stopReader(): void {
    if (!this.readerActive) return;

    const rl = this.attachedReader;
    if (rl !== null && this.lineHandler !== null) {
      rl.removeListener("line", this.lineHandler);
    }

    this.lineHandler = null;
    this.readerActive = false;
    this.attachedReader = null;
  }

  private recordDiagnostic(
    kind: ProtocolDiagnosticKind,
    message: string,
    rawLine: string,
    countsTowardDesync = true,
  ): void {
    const diagnostic: ProtocolDiagnostic = {
      kind,
      message,
      rawLine: previewLine(rawLine),
      timestampMs: Date.now(),
    };
    this.diagnosticBuffer.push(diagnostic);
    this.logger.warn(`[attest.protocol] ${kind}: ${message}`);
    for (const listener of this.diagnosticListeners) {
      try {
        listener(diagnostic);
      } catch (err) {
        // Listener bugs must not break the reader or shadow other subscribers.
        this.logger.error(
          `[attest.protocol] diagnostic listener threw: ${err instanceof Error ? err.message : String(err)}`,
        );
      }
    }
    if (countsTowardDesync) this.maybeTriggerDesync(diagnostic.timestampMs);
  }

  private maybeTriggerDesync(nowMs: number): void {
    if (this.desynced) return;
    const recent = this.diagnosticBuffer.countWithin(this.desyncWindowMs, nowMs);
    if (recent >= this.desyncThreshold) {
      this.desynced = true;
      const snapshot = this.diagnosticBuffer.snapshot();
      const err = new ProtocolDesyncError(
        `Engine protocol desync: ${recent} unroutable response lines within ${this.desyncWindowMs}ms.`,
        snapshot,
      );
      this.logger.error(`[attest.protocol] ${err.message}`);
      this.failAll(err);
    }
  }

  private handleLine(line: string): void {
    let response;
    try {
      response = decodeResponse(line);
    } catch (err) {
      this.handleDecodeFailure(line, err);
      return;
    }

    let reqId: number;
    try {
      reqId = extractId(response);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // An id-less line (a notification) answers no request: drop it. It is logged but never
      // counts toward desync, since the engine may legitimately write one.
      this.recordDiagnostic("missing_id", message, line, false);
      return;
    }

    const pending = this.pending.get(reqId);
    if (pending === undefined) {
      this.recordDiagnostic(
        "unknown_id",
        `no pending request for id=${reqId}`,
        line,
      );
      return;
    }
    this.pending.delete(reqId);

    try {
      const result = extractResult(response);
      pending.resolve(result);
    } catch (err) {
      pending.reject(err);
    }
  }

  private handleDecodeFailure(line: string, err: unknown): void {
    if (err instanceof ProtocolError) {
      // Try to route a JSON-RPC error response to its waiting request.
      let reqId = -1;
      try {
        const raw = JSON.parse(line.trim()) as Record<string, unknown>;
        const idCandidate = raw["id"];
        if (typeof idCandidate === "number" && Number.isFinite(idCandidate)) {
          reqId = idCandidate;
        }
      } catch {
        reqId = -1;
      }
      const pending = this.pending.get(reqId);
      if (pending !== undefined) {
        this.pending.delete(reqId);
        pending.reject(err);
        return;
      }
      this.recordDiagnostic(
        "non_routable_error",
        `engine error code=${err.code} (${err.errorMessage}) had no matching pending id`,
        line,
      );
      return;
    }

    const message = err instanceof Error ? err.message : String(err);
    const kind = classifyDecodeError(message);
    this.recordDiagnostic(kind, message, line);
  }

  /** Reject every request still waiting for a response. */
  rejectPending(err: Error): void {
    this.failAll(err);
  }

  private failAll(err: Error): void {
    for (const pending of this.pending.values()) {
      pending.reject(err);
    }
    this.pending.clear();
  }

  /**
   * Send one request and resolve with the result of the response that carries its id.
   *
   * After a timeout the engine is restarted (its late response can no longer be matched),
   * every other pending request is rejected, and the new engine gets the remaining session
   * budget.
   */
  async sendRequest(
    method: string,
    params: Record<string, unknown>,
    timeoutMs?: number,
  ): Promise<unknown> {
    return this.send(method, params, timeoutMs, true);
  }

  /** `sendRequest` without the restart-on-timeout step, for the engine's own handshake. */
  async sendWithoutRestart(
    method: string,
    params: Record<string, unknown>,
    timeoutMs: number,
  ): Promise<unknown> {
    return this.send(method, params, timeoutMs, false);
  }

  private async send(
    method: string,
    params: Record<string, unknown>,
    timeoutMs: number | undefined,
    restartOnTimeout: boolean,
  ): Promise<unknown> {
    // The engine's own handshake runs inside a restart and must not wait for it.
    if (restartOnTimeout) await this.engine.whenReady?.();
    // Every client over one EngineManager shares that engine's id space and reader.
    const shared = this.engine.client;
    if (shared !== undefined && shared !== this) {
      return restartOnTimeout
        ? shared.sendRequest(method, params, timeoutMs)
        : shared.sendWithoutRestart(method, params, resolveTimeoutMs(timeoutMs));
    }

    if (this.desynced) {
      throw new ProtocolDesyncError(
        "Engine protocol is desynced; AttestClient will not send further requests.",
        this.diagnosticBuffer.snapshot(),
      );
    }

    const resolvedTimeout = resolveTimeoutMs(timeoutMs);
    if (this.engine.isRunning === false) {
      const tail = this.engine.stderrTail ?? "";
      throw new Error(`Engine process is not running.${tail === "" ? "" : ` stderr:\n${tail}`}`);
    }
    const cp = this.engine.childProcess;
    if (cp === null || cp.stdin === null) {
      throw new Error("Engine process not started.");
    }
    this.startReader();

    const reservation = worstCaseReservation(method, params);
    let capturedReqId: number | undefined;
    let timer: NodeJS.Timeout | undefined;

    const requestPromise = new Promise<unknown>((resolve, reject) => {
      // Serialize writes through promise chain
      this.writeChain = this.writeChain.then(() => {
        this.requestId += 1;
        const reqId = this.requestId;
        capturedReqId = reqId;
        this.pending.set(reqId, { resolve, reject, reservation });

        const requestStr = encodeRequest(reqId, method, params);
        cp.stdin!.write(requestStr, (err: Error | null | undefined) => {
          if (err) {
            this.pending.delete(reqId);
            reject(this.describeWriteError(err));
          }
        });
      });
    });
    const timeoutPromise = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new EngineTimeoutError(method, resolvedTimeout)), resolvedTimeout);
    });

    try {
      return await Promise.race([requestPromise, timeoutPromise]);
    } catch (err) {
      if (err instanceof EngineTimeoutError) {
        if (capturedReqId !== undefined) this.pending.delete(capturedReqId);
        if (restartOnTimeout) await this.recoverFromTimeout(method, params, err);
      }
      throw err;
    } finally {
      clearTimeout(timer);
    }
  }

  /** A failed pipe write names the engine's stderr tail; the original error stays as `cause` and `code`. */
  private describeWriteError(err: Error): Error {
    const tail = this.engine.stderrTail ?? "";
    const wrapped = new Error(
      `Failed to write to the engine: ${err.message}.${tail === "" ? "" : ` stderr:\n${tail}`}`,
      { cause: err },
    );
    return Object.assign(wrapped, { code: (err as NodeJS.ErrnoException).code });
  }

  private async recoverFromTimeout(
    method: string,
    params: Record<string, unknown>,
    timeout: EngineTimeoutError,
  ): Promise<void> {
    // A client replaced by an engine restart no longer owns the engine.
    if (this.engine.restartAfterTimeout === undefined || this.engine.client !== this) return;
    const abandoned = [
      worstCaseReservation(method, params),
      ...[...this.pending.values()].map((p) => p.reservation),
    ];
    this.failAll(new Error(`Engine restarted after '${method}' timed out; request abandoned.`));
    try {
      await this.engine.restartAfterTimeout(abandoned);
    } catch (err) {
      throw new Error(
        `Engine request '${method}' timed out and the engine could not be restarted: ` +
          (err instanceof Error ? err.message : String(err)),
        { cause: timeout },
      );
    }
  }

  async evaluateBatch(
    trace: Trace,
    assertions: readonly Assertion[],
    options?: { timeout?: number; budget?: BatchBudget; context?: BatchContext },
  ): Promise<EvaluateBatchResult> {
    // Simulation is decided by the engine: started with ATTEST_SIMULATION
    // set, it evaluates layers 1-4 for real and returns not_evaluated
    // (reason "simulation") for layers 5-6. Refuse to send when the running
    // engine did not advertise that mode: it would call real providers.
    if (isSimulationMode()) {
      // EngineManager keeps the initialize result private.
      requireSimulationCapability(this.engine["initResult"]);
    }
    const params: EvaluateBatchParams = {
      trace,
      assertions: [...assertions],
      ...(options?.budget !== undefined ? { budget: options.budget } : {}),
      ...(options?.context !== undefined ? { context: options.context } : {}),
    };
    let raw: Record<string, unknown>;
    try {
      raw = (await this.sendRequest(
        "evaluate_batch",
        params as unknown as Record<string, unknown>,
        options?.timeout,
      )) as Record<string, unknown>;
    } catch (err) {
      // An RPC error carries no usage, yet the engine may have called the judge first.
      if (err instanceof ProtocolError) this.engine.markUnknownSpend?.();
      throw err;
    }
    const usage = parseUsage(raw["usage"]);
    this.engine.recordUsage?.(usage);
    const result = raw as unknown as EvaluateBatchResult;
    this.requireSentAssertionIds(assertions, result);
    return { ...result, usage };
  }

  /** Reject a response whose assertion ids differ from the ids that were sent. */
  private requireSentAssertionIds(
    assertions: readonly Assertion[],
    result: EvaluateBatchResult,
  ): void {
    const sent = new Set(assertions.map((a) => a.assertion_id));
    const returned = new Set((result.results ?? []).map((r) => r.assertion_id));
    const missing = [...sent].filter((id) => !returned.has(id));
    const unexpected = [...returned].filter((id) => !sent.has(id));
    if (missing.length === 0 && unexpected.length === 0) return;
    throw new ProtocolDesyncError(
      "evaluate_batch response does not match the request: " +
        `missing assertion ids [${missing.join(", ")}], unexpected [${unexpected.join(", ")}].`,
      this.diagnosticBuffer.snapshot(),
    );
  }

  async submitPluginResult(
    traceId: string,
    pluginName: string,
    assertionId: string,
    status: string,
    score: number,
    explanation: string,
    options?: { timeout?: number },
  ): Promise<boolean> {
    const result: AssertionResult = {
      assertion_id: assertionId,
      status,
      score,
      explanation,
      cost: 0.0,
      duration_ms: 0,
    };
    const params = {
      trace_id: traceId,
      plugin_name: pluginName,
      assertion_id: assertionId,
      result,
    };
    const raw = await this.sendRequest(
      "submit_plugin_result",
      params as Record<string, unknown>,
      options?.timeout,
    );
    return Boolean((raw as Record<string, unknown>)?.accepted ?? false);
  }

  async queryDrift(
    assertionId: string,
    windowSize = 50,
    options?: { timeout?: number },
  ): Promise<DriftReport> {
    if (isSimulationMode()) {
      return {
        assertion_id: assertionId,
        mean: 0.0,
        stddev: 0,
        count: 0,
        latest_score: 0.0,
        deviation: 0,
        status: "no_data",
      };
    }

    const raw = await this.sendRequest(
      "query_drift",
      { assertion_id: assertionId, window_size: windowSize },
      options?.timeout,
    );
    return (raw as { report: DriftReport }).report;
  }

  async generateUserMessage(
    persona: SimulatePersona,
    conversationHistory: readonly ConversationMessage[],
    faultConfig?: SimulateFaultConfig,
    options?: { timeout?: number },
  ): Promise<string> {
    if (isSimulationMode()) {
      return `[simulation] Hello from ${persona.name}`;
    }

    const params: Record<string, unknown> = {
      persona,
      conversation_history: conversationHistory,
    };
    if (faultConfig !== undefined) {
      params.fault_config = faultConfig;
    }
    let raw: unknown;
    try {
      raw = await this.sendRequest(
        "generate_user_message",
        params,
        options?.timeout,
      );
    } catch (err) {
      // An engine error can follow the simulated user's model call; that call keeps its reservation.
      if (err instanceof ProtocolError) this.engine.markUnknownSpend?.();
      throw err;
    }
    // The reply carries no usage: the simulated user's model call is spend the SDK cannot see.
    this.engine.markUnknownSpend?.();
    return String((raw as { message: string }).message);
  }
}

/**
 * What the engine may have spent on a request that timed out: its per-batch limits when it
 * sent any, otherwise (null) the whole remaining session budget. Methods that cannot call a
 * model reserve nothing.
 */
function worstCaseReservation(method: string, params: Record<string, unknown>): BudgetReservation {
  if (method === "generate_user_message") return { judgeCalls: null, usd: null };
  if (method !== "evaluate_batch") return { judgeCalls: 0, usd: 0 };
  const assertions = (params["assertions"] ?? []) as readonly Assertion[];
  const judges = assertions.some((a) => a.type === "llm_judge");
  const paid = judges || assertions.some((a) => a.type === "embedding");
  const budget = params["budget"] as BatchBudget | undefined;
  return {
    judgeCalls: judges ? (budget?.max_judge_calls ?? null) : 0,
    usd: paid ? (budget?.max_cost_usd ?? null) : 0,
  };
}

function classifyDecodeError(message: string): ProtocolDiagnosticKind {
  if (message.startsWith("malformed JSON")) return "malformed_json";
  if (message.startsWith("expected JSON object")) return "non_object_response";
  if (message.startsWith("invalid jsonrpc version")) return "invalid_jsonrpc_version";
  if (message.startsWith("empty response line")) return "malformed_json";
  return "malformed_json";
}

/**
 * Throw unless the engine advertised `simulation_mode` at initialize. An
 * engine started without ATTEST_SIMULATION (or older than 1.0) would call
 * real embedding and judge providers in simulation mode.
 */
export function requireSimulationCapability(init: InitializeResult | null | undefined): void {
  if (init === null || init === undefined) {
    throw new Error("simulation mode: the engine has not been initialized");
  }
  if (!(init.capabilities ?? []).includes("simulation_mode")) {
    throw new Error(
      `simulation mode is on but attest-engine ${init.engine_version} was not started in ` +
        "simulation mode (capability 'simulation_mode' missing), so it would call real " +
        "providers. Set ATTEST_SIMULATION=1 before the engine starts (the Vitest fixtures do " +
        "this for you) and use attest-engine 1.0 or later.",
    );
  }
}
