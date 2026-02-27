import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { config, isSimulationMode, resetConfig } from "../../packages/core/src/config.js";
import { simulationEvaluateBatch } from "../../packages/core/src/simulation.js";
import type { Assertion, Trace } from "../../packages/core/src/proto/types.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeAssertion(id: string, type = "schema"): Assertion {
  return { assertion_id: id, type, spec: {} };
}

function makeTrace(): Trace {
  return {
    trace_id: "trc_test001",
    output: { response: "ok" },
    steps: [],
  };
}

// ---------------------------------------------------------------------------
// config API
// ---------------------------------------------------------------------------

describe("config / isSimulationMode / resetConfig", () => {
  afterEach(() => {
    resetConfig();
    delete process.env["ATTEST_SIMULATION"];
  });

  it("defaults to false", () => {
    expect(isSimulationMode()).toBe(false);
  });

  it("config({ simulation: true }) enables simulation mode", () => {
    config({ simulation: true });
    expect(isSimulationMode()).toBe(true);
  });

  it("config({ simulation: false }) disables simulation mode", () => {
    config({ simulation: true });
    config({ simulation: false });
    expect(isSimulationMode()).toBe(false);
  });

  it("resetConfig() resets to false after programmatic enable", () => {
    config({ simulation: true });
    resetConfig();
    expect(isSimulationMode()).toBe(false);
  });

  it("empty options object does not change state", () => {
    config({ simulation: true });
    config({});
    expect(isSimulationMode()).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Environment variable override
// ---------------------------------------------------------------------------

describe("isSimulationMode — env var override", () => {
  afterEach(() => {
    resetConfig();
    delete process.env["ATTEST_SIMULATION"];
  });

  it('ATTEST_SIMULATION=1 enables simulation mode', () => {
    process.env["ATTEST_SIMULATION"] = "1";
    expect(isSimulationMode()).toBe(true);
  });

  it('ATTEST_SIMULATION=true enables simulation mode', () => {
    process.env["ATTEST_SIMULATION"] = "true";
    expect(isSimulationMode()).toBe(true);
  });

  it('ATTEST_SIMULATION=yes enables simulation mode', () => {
    process.env["ATTEST_SIMULATION"] = "yes";
    expect(isSimulationMode()).toBe(true);
  });

  it('ATTEST_SIMULATION=0 does not enable simulation mode', () => {
    process.env["ATTEST_SIMULATION"] = "0";
    expect(isSimulationMode()).toBe(false);
  });

  it('ATTEST_SIMULATION="" does not enable simulation mode', () => {
    process.env["ATTEST_SIMULATION"] = "";
    expect(isSimulationMode()).toBe(false);
  });

  it("env var does not override resetConfig (env is read live)", () => {
    process.env["ATTEST_SIMULATION"] = "1";
    resetConfig(); // resets _simulationMode flag only, not env
    expect(isSimulationMode()).toBe(true); // env still active
  });
});

// ---------------------------------------------------------------------------
// simulationEvaluateBatch
// ---------------------------------------------------------------------------

describe("simulationEvaluateBatch", () => {
  it("returns all-pass results", () => {
    const assertions = [makeAssertion("a1"), makeAssertion("a2")];
    const result = simulationEvaluateBatch(assertions);

    expect(result.results).toHaveLength(2);
    for (const r of result.results) {
      expect(r.status).toBe("pass");
      expect(r.score).toBe(1.0);
    }
  });

  it("preserves assertion IDs in output", () => {
    const assertions = [makeAssertion("assert-xyz-001"), makeAssertion("assert-xyz-002")];
    const result = simulationEvaluateBatch(assertions);

    expect(result.results[0].assertion_id).toBe("assert-xyz-001");
    expect(result.results[1].assertion_id).toBe("assert-xyz-002");
  });

  it("includes [simulation] marker in explanation", () => {
    const assertions = [makeAssertion("a1", "content")];
    const result = simulationEvaluateBatch(assertions);

    expect(result.results[0].explanation).toContain("[simulation]");
  });

  it("reports zero cost and zero duration", () => {
    const assertions = [makeAssertion("a1")];
    const result = simulationEvaluateBatch(assertions);

    expect(result.total_cost).toBe(0);
    expect(result.total_duration_ms).toBe(0);
    expect(result.results[0].cost).toBe(0);
    expect(result.results[0].duration_ms).toBe(0);
  });

  it("handles empty assertions array", () => {
    const result = simulationEvaluateBatch([]);
    expect(result.results).toHaveLength(0);
    expect(result.total_cost).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// AttestClient.evaluateBatch delegates to simulation when enabled
// ---------------------------------------------------------------------------

describe("AttestClient.evaluateBatch — simulation delegation", () => {
  afterEach(() => {
    resetConfig();
    delete process.env["ATTEST_SIMULATION"];
  });

  it("returns simulation result without calling sendRequest", async () => {
    // We test the delegation without a real engine by importing AttestClient
    // and providing a stub EngineManager.
    const { AttestClient } = await import("../../packages/core/src/client.js");

    // Minimal EngineManager stub — sendRequest must never be called.
    const stubEngine = {
      sendRequest: vi.fn().mockRejectedValue(new Error("should not be called")),
      readlineInterface: null,
      childProcess: null,
      isRunning: false,
      start: vi.fn(),
      stop: vi.fn(),
    };

    const client = new AttestClient(stubEngine as never);
    const trace = makeTrace();
    const assertions = [makeAssertion("sim-001", "schema"), makeAssertion("sim-002", "constraint")];

    config({ simulation: true });

    const result = await client.evaluateBatch(trace, assertions);

    expect(stubEngine.sendRequest).not.toHaveBeenCalled();
    expect(result.results).toHaveLength(2);
    expect(result.results[0].assertion_id).toBe("sim-001");
    expect(result.results[1].assertion_id).toBe("sim-002");
    expect(result.results[0].status).toBe("pass");
    expect(result.results[1].status).toBe("pass");
  });

  it("also delegates when ATTEST_SIMULATION env var is set", async () => {
    process.env["ATTEST_SIMULATION"] = "1";

    const { AttestClient } = await import("../../packages/core/src/client.js");

    const stubEngine = {
      sendRequest: vi.fn().mockRejectedValue(new Error("should not be called")),
      readlineInterface: null,
      childProcess: null,
      isRunning: false,
      start: vi.fn(),
      stop: vi.fn(),
    };

    const client = new AttestClient(stubEngine as never);
    const trace = makeTrace();
    const assertions = [makeAssertion("env-001")];

    const result = await client.evaluateBatch(trace, assertions);

    expect(stubEngine.sendRequest).not.toHaveBeenCalled();
    expect(result.results[0].assertion_id).toBe("env-001");
    expect(result.results[0].explanation).toContain("[simulation]");
  });
});
