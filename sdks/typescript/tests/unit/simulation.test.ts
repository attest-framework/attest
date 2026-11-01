import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { config, isSimulationMode, getSampleRate, resetConfig } from "../../packages/core/src/config.js";
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
// getSampleRate
// ---------------------------------------------------------------------------

describe("getSampleRate", () => {
  afterEach(() => {
    resetConfig();
    delete process.env["ATTEST_SAMPLE_RATE"];
  });

  it("defaults to 0", () => {
    expect(getSampleRate()).toBe(0);
  });

  it("config({ sampleRate: 0.1 }) sets rate", () => {
    config({ sampleRate: 0.1 });
    expect(getSampleRate()).toBe(0.1);
  });

  it("config({ sampleRate: 0 }) resets to 0", () => {
    config({ sampleRate: 0.5 });
    config({ sampleRate: 0 });
    expect(getSampleRate()).toBe(0);
  });

  it("env var ATTEST_SAMPLE_RATE is used as fallback", () => {
    process.env["ATTEST_SAMPLE_RATE"] = "0.05";
    expect(getSampleRate()).toBe(0.05);
  });

  it("programmatic value takes priority over env var", () => {
    process.env["ATTEST_SAMPLE_RATE"] = "0.05";
    config({ sampleRate: 0.2 });
    expect(getSampleRate()).toBe(0.2);
  });

  it("resetConfig() clears programmatic value", () => {
    config({ sampleRate: 0.3 });
    resetConfig();
    expect(getSampleRate()).toBe(0);
  });

  it("throws RangeError for sampleRate > 1", () => {
    expect(() => config({ sampleRate: 1.5 })).toThrow(RangeError);
  });

  it("throws RangeError for negative sampleRate", () => {
    expect(() => config({ sampleRate: -0.1 })).toThrow(RangeError);
  });

  it("throws RangeError for invalid env var", () => {
    process.env["ATTEST_SAMPLE_RATE"] = "abc";
    expect(() => getSampleRate()).toThrow(RangeError);
  });

  it("accepts boundary values 0.0 and 1.0", () => {
    config({ sampleRate: 0.0 });
    expect(getSampleRate()).toBe(0);
    config({ sampleRate: 1.0 });
    expect(getSampleRate()).toBe(1.0);
  });
});

// ---------------------------------------------------------------------------
// AttestClient.evaluateBatch: simulation is decided by the engine
// ---------------------------------------------------------------------------

describe("AttestClient.evaluateBatch — simulation goes to the engine", () => {
  afterEach(() => {
    resetConfig();
    delete process.env["ATTEST_SIMULATION"];
  });

  it.each([
    ["config", () => config({ simulation: true })],
    ["env", () => { process.env["ATTEST_SIMULATION"] = "1"; }],
  ])("sends evaluate_batch to the engine (%s)", async (_name, enable) => {
    const { AttestClient } = await import("../../packages/core/src/client.js");
    const client = new AttestClient({} as never);
    const engineResult = {
      results: [
        {
          assertion_id: "sim-001",
          status: "not_evaluated",
          reason: "simulation",
          score: 0,
          explanation: "not run: simulation mode",
        },
      ],
      total_cost: 0,
      total_duration_ms: 0,
    };
    const send = vi.spyOn(client, "sendRequest").mockResolvedValue(engineResult as never);
    enable();

    const result = await client.evaluateBatch(makeTrace(), [makeAssertion("sim-001", "llm_judge")]);

    expect(send).toHaveBeenCalledOnce();
    expect(result.results[0].status).toBe("not_evaluated");
    expect(result.results[0].reason).toBe("simulation");
  });
});
