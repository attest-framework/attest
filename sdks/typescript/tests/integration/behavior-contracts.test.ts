import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  AttestClient,
  EngineManager,
  attestExpect,
} from "../../packages/core/src/index.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

const here = dirname(fileURLToPath(import.meta.url));
const protocolTests = resolve(here, "../../../../protocol-tests");

function loadTrace(path: string): Trace {
  return JSON.parse(readFileSync(join(protocolTests, path), "utf8")) as Trace;
}

describe("behavior contracts on the frozen INC-1042 traces", () => {
  let engine: EngineManager | undefined;
  let client: AttestClient | undefined;
  let skipReason: string | undefined;

  beforeAll(async () => {
    // The engine built by `make engine`; never auto-download from a test run.
    const enginePath = resolve(here, "../../../../bin/attest-engine");
    if (!existsSync(enginePath)) {
      skipReason = "attest-engine binary not found; build with 'make engine'";
      return;
    }
    engine = new EngineManager({ enginePath });
    const initResult = await engine.start();
    expect(initResult.compatible).toBe(true);
    client = new AttestClient(engine);
  });

  afterAll(async () => {
    if (engine) {
      await engine.stop();
    }
  });

  it.each([
    ["py", "trace-v2/otlp/inc-1042-refund.trace.json"],
    ["ts", "contracts/traces/inc-1042-ts.trace.json"],
  ])("reports the double refund in the %s trace", async (_, path, ctx) => {
    if (skipReason || !client) {
      ctx.skip();
      return;
    }
    const trace = loadTrace(path);
    const chain = attestExpect(trace).toolCardinality("issue_refund", {
      per: "order_id",
      atMost: 1,
    });

    const result = await client.evaluateBatch(trace, chain.assertions);

    expect(result.results).toHaveLength(1);
    expect(result.results[0].status).toBe("hard_fail");
    expect(result.results[0].explanation).toBe(
      "issue_refund called 2× for order_id=O-1042 (max 1)",
    );
  });

  it("passes the corrected trace", async (ctx) => {
    if (skipReason || !client) {
      ctx.skip();
      return;
    }
    const trace = loadTrace("contracts/traces/inc-1042-corrected.trace.json");
    const chain = attestExpect(trace).toolCardinality("issue_refund", {
      per: "order_id",
      atMost: 1,
    });

    const result = await client.evaluateBatch(trace, chain.assertions);

    expect(result.results.map((r) => r.status)).toEqual(["pass"]);
  });

  it("fails cycle period 2 on A→B→A→B", async (ctx) => {
    if (skipReason || !client) {
      ctx.skip();
      return;
    }
    const trace = loadTrace("contracts/traces/abab.trace.json");
    const chain = attestExpect(trace).noToolCycles(2);

    const result = await client.evaluateBatch(trace, chain.assertions);

    expect(result.results[0].status).toBe("hard_fail");
    expect(result.results[0].explanation).toMatch(/^tool cycle of period 2 repeated 2×/);
  });
});
