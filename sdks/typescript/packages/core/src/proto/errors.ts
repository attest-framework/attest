import type { ErrorData } from "./types.js";
import type { ProtocolDiagnostic } from "./diagnostics.js";

export class ProtocolError extends Error {
  readonly code: number;
  readonly errorMessage: string;
  readonly data: ErrorData | undefined;

  constructor(code: number, message: string, data?: ErrorData) {
    super(message);
    this.name = "ProtocolError";
    this.code = code;
    this.errorMessage = message;
    this.data = data;
  }
}

export class EngineTimeoutError extends Error {
  readonly method: string;
  readonly timeoutMs: number;

  constructor(method: string, timeoutMs: number) {
    super(
      `Engine request '${method}' timed out after ${timeoutMs}ms. ` +
        "Set ATTEST_ENGINE_TIMEOUT to increase the limit.",
    );
    this.name = "EngineTimeoutError";
    this.method = method;
    this.timeoutMs = timeoutMs;
  }
}

/**
 * Raised when the reader loop detects that engine output has lost
 * framing or contains repeated unparseable lines. All in-flight requests
 * are rejected with this error so callers see a visible failure rather
 * than a hung promise.
 */
export class ProtocolDesyncError extends Error {
  readonly diagnostics: readonly ProtocolDiagnostic[];

  constructor(message: string, diagnostics: readonly ProtocolDiagnostic[]) {
    super(message);
    this.name = "ProtocolDesyncError";
    this.diagnostics = diagnostics;
  }
}
