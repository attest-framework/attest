/**
 * Protocol diagnostic types for client-side observability.
 *
 * The reader loop emits diagnostics whenever it cannot route a response
 * line: malformed JSON, missing id, unknown id, or non-routable protocol
 * errors. Diagnostics are buffered in a bounded ring (32 entries) and
 * trigger a desync error when the rate exceeds the configured threshold.
 */

export type ProtocolDiagnosticKind =
  | "malformed_json"
  | "non_object_response"
  | "invalid_jsonrpc_version"
  | "missing_id"
  | "unknown_id"
  | "non_routable_error";

export interface ProtocolDiagnostic {
  readonly kind: ProtocolDiagnosticKind;
  readonly message: string;
  readonly rawLine: string;
  readonly timestampMs: number;
}

export interface ProtocolLogger {
  warn(message: string, ...args: unknown[]): void;
  error(message: string, ...args: unknown[]): void;
}

const DEFAULT_BUFFER_SIZE = 32;
const MAX_LINE_PREVIEW = 512;

export class ProtocolDiagnosticBuffer {
  private readonly capacity: number;
  private readonly entries: ProtocolDiagnostic[] = [];

  constructor(capacity: number = DEFAULT_BUFFER_SIZE) {
    if (capacity <= 0) {
      throw new Error(
        `ProtocolDiagnosticBuffer capacity must be positive, got ${capacity}`,
      );
    }
    this.capacity = capacity;
  }

  push(diagnostic: ProtocolDiagnostic): void {
    if (this.entries.length >= this.capacity) {
      this.entries.shift();
    }
    this.entries.push(diagnostic);
  }

  snapshot(): readonly ProtocolDiagnostic[] {
    return this.entries.slice();
  }

  countWithin(windowMs: number, nowMs: number): number {
    const cutoff = nowMs - windowMs;
    let count = 0;
    for (let i = this.entries.length - 1; i >= 0; i -= 1) {
      if (this.entries[i]!.timestampMs >= cutoff) {
        count += 1;
      } else {
        break;
      }
    }
    return count;
  }

  clear(): void {
    this.entries.length = 0;
  }
}

export function previewLine(line: string): string {
  if (line.length <= MAX_LINE_PREVIEW) return line;
  return line.slice(0, MAX_LINE_PREVIEW) + "…";
}
