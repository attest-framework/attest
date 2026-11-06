import { describe, it, expect } from "vitest";
import {
  encodeRequest,
  decodeResponse,
  extractResult,
  extractId,
  ProtocolError,
} from "../../packages/core/src/proto/index.js";

describe("encodeRequest", () => {
  it("produces valid NDJSON with jsonrpc 2.0 fields", () => {
    const line = encodeRequest(1, "initialize", { sdk_name: "test" });
    expect(line.endsWith("\n")).toBe(true);

    const parsed = JSON.parse(line);
    expect(parsed.jsonrpc).toBe("2.0");
    expect(parsed.id).toBe(1);
    expect(parsed.method).toBe("initialize");
    expect(parsed.params).toEqual({ sdk_name: "test" });
  });

  it("produces compact JSON without extra whitespace", () => {
    const line = encodeRequest(2, "shutdown", {});
    const trimmed = line.trimEnd();
    // re-parse to verify it round-trips
    const parsed = JSON.parse(trimmed);
    expect(parsed.id).toBe(2);
    expect(parsed.method).toBe("shutdown");
  });
});

describe("decodeResponse", () => {
  it("parses a valid success response", () => {
    const raw = JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      result: { engine_version: "0.3.0" },
    });

    const response = decodeResponse(raw);
    expect(response.jsonrpc).toBe("2.0");
    expect(response.id).toBe(1);
    expect(response.result).toEqual({ engine_version: "0.3.0" });
  });

  it("throws Error on empty input", () => {
    expect(() => decodeResponse("")).toThrow("empty response line");
    expect(() => decodeResponse("  ")).toThrow("empty response line");
  });

  it("throws Error on malformed JSON", () => {
    expect(() => decodeResponse("{invalid")).toThrow("malformed JSON response");
  });

  it("throws Error on non-object JSON", () => {
    expect(() => decodeResponse("[1,2,3]")).toThrow("expected JSON object");
    expect(() => decodeResponse('"string"')).toThrow("expected JSON object");
  });

  it("throws Error on invalid jsonrpc version", () => {
    const raw = JSON.stringify({ jsonrpc: "1.0", id: 1, result: {} });
    expect(() => decodeResponse(raw)).toThrow("invalid jsonrpc version");
  });

  it("throws ProtocolError on error response", () => {
    const raw = JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      error: {
        code: 1001,
        message: "invalid trace",
        data: {
          error_type: "validation",
          retryable: false,
          detail: "trace_id is required",
        },
      },
    });

    try {
      decodeResponse(raw);
      expect.unreachable("should have thrown");
    } catch (e) {
      expect(e).toBeInstanceOf(ProtocolError);
      const pe = e as ProtocolError;
      expect(pe.code).toBe(1001);
      expect(pe.errorMessage).toBe("invalid trace");
      expect(pe.data).toEqual({
        error_type: "validation",
        retryable: false,
        detail: "trace_id is required",
      });
    }
  });

  it("throws ProtocolError without data when error has no data", () => {
    const raw = JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      error: { code: 3001, message: "engine error" },
    });

    try {
      decodeResponse(raw);
      expect.unreachable("should have thrown");
    } catch (e) {
      expect(e).toBeInstanceOf(ProtocolError);
      const pe = e as ProtocolError;
      expect(pe.code).toBe(3001);
      expect(pe.errorMessage).toBe("engine error");
      expect(pe.data).toBeUndefined();
    }
  });
});

describe("extractResult", () => {
  it("returns the result field from a response", () => {
    const response = decodeResponse(
      JSON.stringify({ jsonrpc: "2.0", id: 1, result: { ok: true } }),
    );
    expect(extractResult(response)).toEqual({ ok: true });
  });

  it("throws when result field is missing", () => {
    // Construct a response object without result (bypassing decode)
    const response = { jsonrpc: "2.0" as const, id: 1 };
    expect(() => extractResult(response)).toThrow("response missing 'result' field");
  });
});

describe("extractId", () => {
  it("returns the id from a response", () => {
    const response = decodeResponse(
      JSON.stringify({ jsonrpc: "2.0", id: 42, result: {} }),
    );
    expect(extractId(response)).toBe(42);
  });

  it("throws when id field is missing", () => {
    // Force undefined id
    const broken = { jsonrpc: "2.0" as const, id: undefined as unknown as number };
    expect(() => extractId(broken)).toThrow("response missing 'id' field");
  });
});
