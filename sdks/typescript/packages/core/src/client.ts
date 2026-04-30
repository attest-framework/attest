import type {
  Assertion,
  ConversationMessage,
  DriftReport,
  EvaluateBatchResult,
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
import type { EngineManager } from "./engine-manager.js";
import { isSimulationMode } from "./config.js";
import { simulationEvaluateBatch } from "./simulation.js";

const DEFAULT_TIMEOUT_MS = 30_000;
const DEFAULT_DIAGNOSTIC_BUFFER = 32;
const DEFAULT_DESYNC_THRESHOLD = 3;
const DEFAULT_DESYNC_WINDOW_MS = 1_000;

function resolveTimeoutMs(override?: number): number {
  if (override !== undefined) return override;
  const env = process.env["ATTEST_ENGINE_TIMEOUT"];
  if (env !== undefined && env !== "") {
    const parsed = Number(env);
    if (!Number.isFinite(parsed) || parsed <= 0) {
      throw new Error(
        `ATTEST_ENGINE_TIMEOUT must be a positive number of milliseconds, got: '${env}'`,
      );
    }
    return parsed;
  }
  return DEFAULT_TIMEOUT_MS;
}

function makeTimeoutPromise(method: string, timeoutMs: number): Promise<never> {
  return new Promise<never>((_, reject) => {
    setTimeout(() => {
      reject(new EngineTimeoutError(method, timeoutMs));
    }, timeoutMs);
  });
}

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
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

export class AttestClient {
  private readonly engine: EngineManager;
  private requestId = 0;
  private readonly pending = new Map<number, PendingRequest>();
  private writeChain: Promise<void> = Promise.resolve();
  private readerActive = false;
  private lineHandler: ((line: string) => void) | null = null;

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

  startReader(): void {
    if (this.readerActive) return;

    const rl = this.engine.readlineInterface;
    if (rl === null) {
      throw new Error("Engine readline interface not available. Call engine.start() first.");
    }

    this.readerActive = true;
    this.lineHandler = (line: string) => this.handleLine(line);
    rl.on("line", this.lineHandler);

    rl.on("close", () => {
      this.failAll(new Error("Engine closed stdout."));
      this.readerActive = false;
    });
  }

  stopReader(): void {
    if (!this.readerActive) return;

    const rl = this.engine.readlineInterface;
    if (rl !== null && this.lineHandler !== null) {
      rl.removeListener("line", this.lineHandler);
    }

    this.lineHandler = null;
    this.readerActive = false;
  }

  private recordDiagnostic(
    kind: ProtocolDiagnosticKind,
    message: string,
    rawLine: string,
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
      listener(diagnostic);
    }
    this.maybeTriggerDesync(diagnostic.timestampMs);
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
      this.recordDiagnostic("missing_id", message, line);
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

  private failAll(err: Error): void {
    for (const pending of this.pending.values()) {
      pending.reject(err);
    }
    this.pending.clear();
  }

  async sendRequest(
    method: string,
    params: Record<string, unknown>,
    timeoutMs?: number,
  ): Promise<unknown> {
    if (this.desynced) {
      throw new ProtocolDesyncError(
        "Engine protocol is desynced; AttestClient will not send further requests.",
        this.diagnosticBuffer.snapshot(),
      );
    }

    const resolvedTimeout = resolveTimeoutMs(timeoutMs);

    if (!this.readerActive) {
      // Delegate to engine sequential mode with timeout
      return Promise.race([
        this.engine.sendRequest(method, params),
        makeTimeoutPromise(method, resolvedTimeout),
      ]);
    }

    const cp = this.engine.childProcess;
    if (cp === null || cp.stdin === null) {
      throw new Error("Engine process not started.");
    }

    let capturedReqId: number | undefined;

    const requestPromise = new Promise<unknown>((resolve, reject) => {
      // Serialize writes through promise chain
      this.writeChain = this.writeChain.then(() => {
        this.requestId += 1;
        const reqId = this.requestId;
        capturedReqId = reqId;
        this.pending.set(reqId, { resolve, reject });

        const requestStr = encodeRequest(reqId, method, params);
        cp.stdin!.write(requestStr, (err: Error | null | undefined) => {
          if (err) {
            this.pending.delete(reqId);
            reject(err);
          }
        });
      });
    });

    const timeoutPromise = makeTimeoutPromise(method, resolvedTimeout);

    return Promise.race([requestPromise, timeoutPromise]).catch((err: unknown) => {
      if (err instanceof EngineTimeoutError && capturedReqId !== undefined) {
        this.pending.delete(capturedReqId);
      }
      throw err;
    });
  }

  async evaluateBatch(
    trace: Trace,
    assertions: readonly Assertion[],
    options?: { timeout?: number },
  ): Promise<EvaluateBatchResult> {
    if (isSimulationMode()) {
      return simulationEvaluateBatch(assertions);
    }

    const params = {
      trace,
      assertions: [...assertions],
    };
    const raw = await this.sendRequest(
      "evaluate_batch",
      params as Record<string, unknown>,
      options?.timeout,
    );
    return raw as EvaluateBatchResult;
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
        mean: 1.0,
        stddev: 0,
        count: 0,
        latest_score: 1.0,
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
    const raw = await this.sendRequest(
      "generate_user_message",
      params,
      options?.timeout,
    );
    return String((raw as { message: string }).message);
  }
}

function classifyDecodeError(message: string): ProtocolDiagnosticKind {
  if (message.startsWith("malformed JSON")) return "malformed_json";
  if (message.startsWith("expected JSON object")) return "non_object_response";
  if (message.startsWith("invalid jsonrpc version")) return "invalid_jsonrpc_version";
  if (message.startsWith("empty response line")) return "malformed_json";
  return "malformed_json";
}
