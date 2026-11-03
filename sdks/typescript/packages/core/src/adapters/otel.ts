/**
 * OpenTelemetry trace adapter.
 *
 * OTLP → Attest trace conversion runs in the engine (`attest-engine trace convert`), the
 * single implementation of the OpenTelemetry GenAI semantic convention mapping shared by
 * every SDK. This module only encodes spans as OTLP/JSON, calls the engine and returns the
 * trace v2 it prints; it holds no mapping of its own. See `docs/guides/trace-check.md`.
 */
import { spawn } from "node:child_process";
import { resolveEngine } from "../engine-manager.js";
import { OTLPConversionError } from "../proto/errors.js";
import type { Trace } from "../proto/types.js";

/** Upper bound for one `attest-engine trace convert` run. */
const CONVERT_TIMEOUT_MS = 60_000;
/** Largest converted trace accepted from the engine (its trace limit is 10 MB). */
const MAX_TRACE_OUTPUT_BYTES = 64 * 1024 * 1024;
/** OTLP status code for an error span. */
const STATUS_CODE_ERROR = 2;

type AttributeValue =
  | string
  | number
  | boolean
  | readonly (string | number | boolean | null | undefined)[];

/** The parts of an `@opentelemetry/sdk-trace-base` `ReadableSpan` the adapter reads. */
export interface OTelSpan {
  readonly name: string;
  /** `[seconds, nanoseconds]` */
  readonly startTime: readonly [number, number];
  readonly endTime: readonly [number, number];
  readonly attributes?: Readonly<Record<string, AttributeValue | undefined>>;
  /** Parent span id (sdk-trace-base 1.x). */
  readonly parentSpanId?: string;
  /** Parent span context (sdk-trace-base 2.x). */
  readonly parentSpanContext?: { readonly spanId: string };
  readonly status?: { readonly code: number; readonly message?: string };
  spanContext(): { readonly traceId: string; readonly spanId: string };
}

/** An OTLP/JSON export: a parsed `ExportTraceServiceRequest`, its JSON text, or its bytes. */
export type OTLPInput = Record<string, unknown> | string | Uint8Array;

export class OTelAdapter {
  /**
   * Build a trace from finished OpenTelemetry spans of one trace.
   *
   * @param agentId Trace `agent_id`; defaults to the first turn's `gen_ai.agent.name`.
   * @throws OTLPConversionError when the engine rejects the spans.
   */
  static async fromSpans(spans: readonly OTelSpan[], agentId?: string): Promise<Trace> {
    return OTelAdapter.fromOtlp(spansToOtlp(spans), agentId);
  }

  /**
   * Convert an OTLP/JSON export (`resourceSpans` → `scopeSpans` → `spans`).
   *
   * @param agentId Trace `agent_id`; defaults to the first turn's `gen_ai.agent.name`.
   * @throws OTLPConversionError when the engine rejects the export (malformed JSON, invalid
   *   ids or timestamps, more than one trace, non-numeric token counts, ...).
   * @throws RangeError when a parsed export holds a non-finite number.
   * @throws OTLPConversionError when the engine times out, overflows the output buffer or
   *   prints no trace.
   * @throws Error when the engine cannot start.
   */
  static async fromOtlp(otlp: OTLPInput, agentId?: string): Promise<Trace> {
    const payload =
      typeof otlp === "string" || otlp instanceof Uint8Array
        ? otlp
        : JSON.stringify(otlp, (_key, v: unknown) => {
            if (typeof v === "number" && !Number.isFinite(v)) {
              throw new RangeError(`OTLP export holds a non-finite number: ${String(v)}`);
            }
            return v;
          });
    const args = ["trace", "convert"];
    if (agentId !== undefined) args.push("--agent-id", agentId);
    args.push("-");
    const stdout = await runConvert(await resolveEngine(), args, payload);
    try {
      return JSON.parse(stdout) as Trace;
    } catch (err) {
      throw new OTLPConversionError(
        `attest-engine trace convert printed output that is not JSON: ${String(err)}`,
      );
    }
  }
}

/**
 * Run `attest-engine trace convert` with `payload` on stdin and return its stdout.
 *
 * Asynchronous on purpose: `spawnSync` with `input` occasionally never returns under
 * parallel load (observed on macOS: about 1 call in 100 with 8 concurrent callers, each
 * hanging until the timeout), and it blocks the event loop while the engine runs.
 */
function runConvert(engine: string, args: string[], payload: string | Uint8Array): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn(engine, args, { stdio: ["pipe", "pipe", "pipe"] });
    let timer: NodeJS.Timeout | undefined;
    // Registered first: a spawn that fails (ENOENT, or EMFILE/ENFILE under load) emits
    // "error" on the next tick, and with EMFILE/ENFILE its stdio streams are null.
    child.on("error", (e) => {
      clearTimeout(timer);
      reject(e);
    });
    const { stdin, stdout, stderr } = child;
    if (stdin === null || stdout === null || stderr === null) return;
    const out: Buffer[] = [];
    const err: Buffer[] = [];
    let outBytes = 0;
    let failure: Error | null = null;
    const fail = (e: Error): void => {
      failure ??= e;
      child.kill("SIGKILL");
    };
    timer = setTimeout(() => {
      fail(
        new OTLPConversionError(
          `attest-engine trace convert timed out after ${CONVERT_TIMEOUT_MS / 1000}s`,
        ),
      );
    }, CONVERT_TIMEOUT_MS);
    stdout.on("data", (chunk: Buffer) => {
      outBytes += chunk.length;
      if (outBytes > MAX_TRACE_OUTPUT_BYTES) {
        fail(
          new OTLPConversionError(
            `attest-engine trace convert printed more than ${MAX_TRACE_OUTPUT_BYTES} bytes`,
          ),
        );
        return;
      }
      out.push(chunk);
    });
    stderr.on("data", (chunk: Buffer) => err.push(chunk));
    // An engine that exits before reading all of stdin (for example on a usage error)
    // makes the write fail with EPIPE; its exit status and stderr report the real error.
    stdin.on("error", () => undefined);
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      if (failure !== null) {
        reject(failure);
        return;
      }
      if (code !== 0) {
        const message = Buffer.concat(err).toString("utf8").trim();
        reject(
          new OTLPConversionError(
            message || `attest-engine trace convert exited ${String(code ?? signal)}`,
          ),
        );
        return;
      }
      resolve(Buffer.concat(out).toString("utf8"));
    });
    stdin.end(payload);
  });
}

function hrTimeToNanos(time: readonly [number, number], spanName: string): string {
  if (!Number.isSafeInteger(time[0]) || !Number.isSafeInteger(time[1])) {
    throw new RangeError(`span ${JSON.stringify(spanName)} has a non-integer HrTime`);
  }
  return (BigInt(time[0]) * 1_000_000_000n + BigInt(time[1])).toString();
}

function anyValue(value: AttributeValue | null | undefined): Record<string, unknown> {
  if (value === null || value === undefined) return {};
  if (typeof value === "boolean") return { boolValue: value };
  if (typeof value === "string") return { stringValue: value };
  if (typeof value === "number") {
    if (!Number.isFinite(value)) {
      throw new RangeError(`OTel attribute value must be finite, got ${String(value)}`);
    }
    return Number.isSafeInteger(value) ? { intValue: String(value) } : { doubleValue: value };
  }
  return { arrayValue: { values: value.map((v) => anyValue(v)) } };
}

/** Encode spans as an OTLP/JSON `ExportTraceServiceRequest`. */
export function spansToOtlp(spans: readonly OTelSpan[]): Record<string, unknown> {
  const encoded = spans.map((span) => {
    const ctx = span.spanContext();
    const out: Record<string, unknown> = {
      traceId: ctx.traceId,
      spanId: ctx.spanId,
      name: span.name,
      startTimeUnixNano: hrTimeToNanos(span.startTime, span.name),
      endTimeUnixNano: hrTimeToNanos(span.endTime, span.name),
      attributes: Object.entries(span.attributes ?? {})
        .filter(([, v]) => v !== undefined)
        .map(([key, v]) => ({ key, value: anyValue(v) })),
    };
    const parent = span.parentSpanContext?.spanId ?? span.parentSpanId;
    if (parent) out["parentSpanId"] = parent;
    if (span.status?.code === STATUS_CODE_ERROR) {
      out["status"] = { code: STATUS_CODE_ERROR, message: span.status.message ?? "" };
    }
    return out;
  });
  return { resourceSpans: [{ scopeSpans: [{ spans: encoded }] }] };
}
