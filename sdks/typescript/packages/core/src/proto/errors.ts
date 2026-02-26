import type { ErrorData } from "./types.js";

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
