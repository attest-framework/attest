/**
 * T13 — @attest-ai/vitest unit tests.
 *
 * Tests: AttestEngineFixture, attestGlobalSetup, AttestCostReporter, filterByTier.
 * All engine interactions are mocked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AttestEngineFixture } from "../src/fixtures.js";
import { attestGlobalSetup } from "../src/plugin.js";
import { AttestCostReporter } from "../src/reporter.js";
import { filterByTier } from "../src/tier-filter.js";

// ---------------------------------------------------------------------------
// Mock @attest-ai/core — replace EngineManager and AttestClient
// ---------------------------------------------------------------------------

vi.mock("@attest-ai/core", () => {
  const mockEvaluateBatch = vi.fn().mockResolvedValue({
    results: [
      {
        assertion_id: "a1",
        status: "pass",
        score: 1.0,
        explanation: "ok",
        cost: 0.001,
        duration_ms: 10,
      },
    ],
    total_cost: 0.001,
    total_duration_ms: 10,
  });

  class MockEngineManager {
    start = vi.fn().mockResolvedValue({
      compatible: true,
      engine_version: "0.4.0",
    });
    stop = vi.fn().mockResolvedValue(undefined);
  }

  class MockAttestClient {
    evaluateBatch = mockEvaluateBatch;
    sendRequest = vi.fn();
  }

  class MockAgentResult {
    constructor(
      public trace: unknown,
      public results: unknown[],
      public totalCost: number,
      public totalDurationMs: number,
    ) {}
  }

  return {
    EngineManager: MockEngineManager,
    AttestClient: MockAttestClient,
    AgentResult: MockAgentResult,
    STATUS_SOFT_FAIL: "soft_fail",
  };
});

// ---------------------------------------------------------------------------
// AttestEngineFixture
// ---------------------------------------------------------------------------

describe("AttestEngineFixture", () => {
  beforeEach(() => {
    globalThis.__attest_session_cost__ = 0;
    globalThis.__attest_session_soft_failures__ = 0;
  });

  it("starts and stops without error", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();
    await fixture.stop();
  });

  it("client getter throws before start()", () => {
    const fixture = new AttestEngineFixture();
    expect(() => fixture.client).toThrow("Engine not started");
  });

  it("client getter returns AttestClient after start()", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();
    expect(fixture.client).toBeDefined();
    await fixture.stop();
  });

  it("start() throws after stop()", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();
    await fixture.stop();
    await expect(fixture.start()).rejects.toThrow("already stopped");
  });

  it("double stop() is safe", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();
    await fixture.stop();
    await fixture.stop(); // should not throw
  });

  it("evaluate() returns AgentResult and accumulates session cost", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();

    const chain = {
      trace: { trace_id: "trc_1", output: {}, steps: [] },
      assertions: [{ assertion_id: "a1", type: "schema", spec: {} }],
    };

    const result = await fixture.evaluate(chain as never);
    expect(result).toBeDefined();
    expect(globalThis.__attest_session_cost__).toBeGreaterThan(0);

    await fixture.stop();
  });

  it("evaluate() throws when budget exceeded", async () => {
    globalThis.__attest_session_cost__ = 0.99;

    const fixture = new AttestEngineFixture();
    await fixture.start();

    const chain = {
      trace: { trace_id: "trc_1", output: {}, steps: [] },
      assertions: [{ assertion_id: "a1", type: "schema", spec: {} }],
    };

    await expect(fixture.evaluate(chain as never, { budget: 0.5 })).rejects.toThrow(
      "budget exceeded",
    );

    await fixture.stop();
  });

  it("accepts enginePath and logLevel options", () => {
    const fixture = new AttestEngineFixture({
      enginePath: "/custom/engine",
      logLevel: "debug",
    });
    expect(fixture).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// attestGlobalSetup
// ---------------------------------------------------------------------------

describe("attestGlobalSetup", () => {
  afterEach(() => {
    globalThis.__attest_engine__ = undefined;
    globalThis.__attest_client__ = undefined;
  });

  it("returns object with setup and teardown functions", () => {
    const hooks = attestGlobalSetup();
    expect(typeof hooks.setup).toBe("function");
    expect(typeof hooks.teardown).toBe("function");
  });

  it("setup() sets global engine and client", async () => {
    const hooks = attestGlobalSetup();
    await hooks.setup();

    expect(globalThis.__attest_engine__).toBeDefined();
    expect(globalThis.__attest_client__).toBeDefined();
    expect(globalThis.__attest_session_cost__).toBe(0);
    expect(globalThis.__attest_session_soft_failures__).toBe(0);

    await hooks.teardown();
  });

  it("teardown() clears global engine and client", async () => {
    const hooks = attestGlobalSetup();
    await hooks.setup();
    await hooks.teardown();

    expect(globalThis.__attest_engine__).toBeUndefined();
    expect(globalThis.__attest_client__).toBeUndefined();
  });

  it("accepts options", async () => {
    const hooks = attestGlobalSetup({
      enginePath: "/custom/engine",
      logLevel: "debug",
    });
    await hooks.setup();
    expect(globalThis.__attest_engine__).toBeDefined();
    await hooks.teardown();
  });

  it("teardown() is safe to call without setup()", async () => {
    const hooks = attestGlobalSetup();
    await hooks.teardown(); // should not throw
  });
});

// ---------------------------------------------------------------------------
// AttestCostReporter
// ---------------------------------------------------------------------------

describe("AttestCostReporter", () => {
  beforeEach(() => {
    globalThis.__attest_session_cost__ = 0;
    globalThis.__attest_session_soft_failures__ = 0;
  });

  it("outputs cost report with zero cost", () => {
    const reporter = new AttestCostReporter();
    let output = "";

    reporter.onFinished([], [], {
      printInfo: (msg: string) => {
        output = msg;
      },
    });

    expect(output).toContain("Attest Cost Report");
    expect(output).toContain("$0.000000 USD");
    expect(output).toContain("Soft failures recorded:");
  });

  it("outputs cost report with accumulated cost", () => {
    globalThis.__attest_session_cost__ = 0.042;
    globalThis.__attest_session_soft_failures__ = 3;

    const reporter = new AttestCostReporter();
    let output = "";

    reporter.onFinished([], [], {
      printInfo: (msg: string) => {
        output = msg;
      },
    });

    expect(output).toContain("$0.042000 USD");
    expect(output).toContain("3");
  });

  it("falls back to process.stdout.write when no context", () => {
    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);

    const reporter = new AttestCostReporter();
    reporter.onFinished([], []);

    expect(writeSpy).toHaveBeenCalledTimes(1);
    expect(writeSpy.mock.calls[0][0]).toContain("Attest Cost Report");

    writeSpy.mockRestore();
  });

  it("report contains separator lines", () => {
    const reporter = new AttestCostReporter();
    let output = "";

    reporter.onFinished([], [], {
      printInfo: (msg: string) => {
        output = msg;
      },
    });

    expect(output).toContain("=".repeat(50));
  });
});

// ---------------------------------------------------------------------------
// filterByTier
// ---------------------------------------------------------------------------

describe("filterByTier", () => {
  it("returns all items when none have tier metadata", () => {
    const items = [
      { name: "test1", fn: () => {} },
      { name: "test2", fn: () => {} },
    ];

    expect(filterByTier(items, 1)).toHaveLength(2);
  });

  it("includes items with tier <= maxTier", () => {
    const fn1 = Object.assign(() => {}, { _attest_tier: 1 });
    const fn2 = Object.assign(() => {}, { _attest_tier: 2 });
    const fn3 = Object.assign(() => {}, { _attest_tier: 3 });

    const items = [
      { name: "tier1", fn: fn1 },
      { name: "tier2", fn: fn2 },
      { name: "tier3", fn: fn3 },
    ];

    const result = filterByTier(items, 2);
    expect(result).toHaveLength(2);
    expect(result.map((i) => i.name)).toEqual(["tier1", "tier2"]);
  });

  it("excludes items with tier > maxTier", () => {
    const fn = Object.assign(() => {}, { _attest_tier: 3 });
    const items = [{ name: "tier3", fn }];

    expect(filterByTier(items, 1)).toHaveLength(0);
  });

  it("includes items with no fn property", () => {
    const items = [{ name: "no-fn" }];
    expect(filterByTier(items, 1)).toHaveLength(1);
  });

  it("includes items with fn but no _attest_tier", () => {
    const items = [{ name: "no-tier", fn: () => {} }];
    expect(filterByTier(items, 1)).toHaveLength(1);
  });

  it("handles empty array", () => {
    expect(filterByTier([], 1)).toHaveLength(0);
  });

  it("handles maxTier of 0", () => {
    const fn = Object.assign(() => {}, { _attest_tier: 1 });
    const items = [{ name: "tier1", fn }];
    expect(filterByTier(items, 0)).toHaveLength(0);
  });

  it("preserves original item references", () => {
    const fn = Object.assign(() => {}, { _attest_tier: 1 });
    const original = { name: "test", fn };
    const result = filterByTier([original], 1);
    expect(result[0]).toBe(original);
  });
});
