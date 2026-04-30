import { EventEmitter } from "node:events";
import { describe, it, expect, vi } from "vitest";
import {
  ProtocolDiagnosticBuffer,
  ProtocolDesyncError,
  type ProtocolDiagnostic,
  type ProtocolLogger,
} from "../../packages/core/src/proto/index.js";
import { AttestClient } from "../../packages/core/src/client.js";

interface FakeReadline extends EventEmitter {
  on(event: string, listener: (...args: unknown[]) => void): this;
  removeListener(event: string, listener: (...args: unknown[]) => void): this;
}

interface FakeEngineLike {
  readlineInterface: FakeReadline;
  childProcess: null;
  sendRequest(method: string, params: Record<string, unknown>): Promise<unknown>;
}

function makeFakeEngine(): FakeEngineLike {
  const rl = new EventEmitter() as FakeReadline;
  return {
    readlineInterface: rl,
    childProcess: null,
    sendRequest: vi.fn(async () => ({})),
  };
}

function silentLogger(): ProtocolLogger {
  return { warn: vi.fn(), error: vi.fn() };
}

describe("ProtocolDiagnosticBuffer", () => {
  it("rejects non-positive capacity", () => {
    expect(() => new ProtocolDiagnosticBuffer(0)).toThrow(/positive/);
    expect(() => new ProtocolDiagnosticBuffer(-1)).toThrow(/positive/);
  });

  it("retains only the most recent entries up to capacity", () => {
    const buf = new ProtocolDiagnosticBuffer(3);
    for (let i = 0; i < 5; i += 1) {
      buf.push({
        kind: "malformed_json",
        message: `m${i}`,
        rawLine: `r${i}`,
        timestampMs: i,
      });
    }
    const snap = buf.snapshot();
    expect(snap.length).toBe(3);
    expect(snap.map((d) => d.message)).toEqual(["m2", "m3", "m4"]);
  });

  it("counts entries within the trailing window only", () => {
    const buf = new ProtocolDiagnosticBuffer(10);
    const base = 1_000_000;
    const entries: ProtocolDiagnostic[] = [
      { kind: "malformed_json", message: "old", rawLine: "", timestampMs: base },
      { kind: "malformed_json", message: "mid", rawLine: "", timestampMs: base + 500 },
      { kind: "malformed_json", message: "near", rawLine: "", timestampMs: base + 950 },
      { kind: "malformed_json", message: "new", rawLine: "", timestampMs: base + 999 },
    ];
    for (const e of entries) buf.push(e);

    expect(buf.countWithin(1_000, base + 999)).toBe(4);
    expect(buf.countWithin(50, base + 999)).toBe(2);
    expect(buf.countWithin(0, base + 999)).toBe(1);
  });
});

describe("AttestClient protocol diagnostics", () => {
  it("records a diagnostic for malformed JSON", () => {
    const engine = makeFakeEngine();
    const logger = silentLogger();
    const client = new AttestClient(engine as never, { logger });
    client.startReader();

    engine.readlineInterface.emit("line", "{not json");

    const diags = client.protocolDiagnostics();
    expect(diags.length).toBe(1);
    expect(diags[0]!.kind).toBe("malformed_json");
    expect(logger.warn).toHaveBeenCalledOnce();
  });

  it("records a diagnostic for missing id", () => {
    const engine = makeFakeEngine();
    const client = new AttestClient(engine as never, { logger: silentLogger() });
    client.startReader();

    engine.readlineInterface.emit(
      "line",
      JSON.stringify({ jsonrpc: "2.0", result: { ok: true } }),
    );

    const diags = client.protocolDiagnostics();
    expect(diags.length).toBe(1);
    expect(diags[0]!.kind).toBe("missing_id");
  });

  it("records a diagnostic for non-routable error responses", () => {
    const engine = makeFakeEngine();
    const client = new AttestClient(engine as never, { logger: silentLogger() });
    client.startReader();

    engine.readlineInterface.emit(
      "line",
      JSON.stringify({
        jsonrpc: "2.0",
        id: 99999,
        error: { code: 3001, message: "engine error" },
      }),
    );

    const diags = client.protocolDiagnostics();
    expect(diags.length).toBe(1);
    expect(diags[0]!.kind).toBe("non_routable_error");
  });

  it("invokes diagnostic listeners", () => {
    const engine = makeFakeEngine();
    const listener = vi.fn();
    const client = new AttestClient(engine as never, {
      logger: silentLogger(),
      onDiagnostic: listener,
    });
    client.startReader();

    engine.readlineInterface.emit("line", "{not json");

    expect(listener).toHaveBeenCalledOnce();
    const diag = listener.mock.calls[0]![0] as ProtocolDiagnostic;
    expect(diag.kind).toBe("malformed_json");
  });

  it("rejects pending requests with ProtocolDesyncError after threshold breach", async () => {
    const engine = makeFakeEngine();
    const logger = silentLogger();
    const client = new AttestClient(engine as never, {
      logger,
      desyncThreshold: 3,
      desyncWindowMs: 1_000,
    });
    client.startReader();

    const pending = new Promise<unknown>((resolve, reject) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (client as any).pending.set(1, { resolve, reject });
    });

    for (let i = 0; i < 3; i += 1) {
      engine.readlineInterface.emit("line", "{not json");
    }

    await expect(pending).rejects.toBeInstanceOf(ProtocolDesyncError);
    expect(logger.error).toHaveBeenCalled();
  });

  it("blocks new requests once desynced", async () => {
    const engine = makeFakeEngine();
    const client = new AttestClient(engine as never, {
      logger: silentLogger(),
      desyncThreshold: 1,
      desyncWindowMs: 1_000,
    });
    client.startReader();

    engine.readlineInterface.emit("line", "{not json");

    await expect(client.sendRequest("noop", {})).rejects.toBeInstanceOf(
      ProtocolDesyncError,
    );
  });

  it("logger respects ATTEST_DEBUG_PROTOCOL gating when no logger is provided", () => {
    const engine = makeFakeEngine();
    const original = process.env["ATTEST_DEBUG_PROTOCOL"];
    delete process.env["ATTEST_DEBUG_PROTOCOL"];
    try {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => undefined);
      const client = new AttestClient(engine as never);
      client.startReader();
      engine.readlineInterface.emit("line", "{not json");
      expect(warnSpy).not.toHaveBeenCalled();
      warnSpy.mockRestore();
    } finally {
      if (original !== undefined) process.env["ATTEST_DEBUG_PROTOCOL"] = original;
    }
  });
});
