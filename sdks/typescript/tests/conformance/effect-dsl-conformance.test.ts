/**
 * Cross-SDK effect fixtures (protocol-tests/contracts/effect-dsl.json). Each case names a
 * DSL call after `world(snapshot)`; the TypeScript chain must emit exactly the fixture's
 * spec, with the case's snapshot (or null) as `world`. The Python SDK checks the same
 * specs, and the engine evaluates them.
 */
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { attestExpect } from "../../packages/core/src/expect.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

interface EffectCase {
  name: string;
  world: string | null;
  typescript: { method: string; args: unknown[] };
  spec: Record<string, unknown>;
}

interface EffectFixture {
  worlds: Record<string, Record<string, unknown>>;
  cases: EffectCase[];
}

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../protocol-tests");
const fixture = JSON.parse(readFileSync(join(root, "contracts", "effect-dsl.json"), "utf8")) as EffectFixture;
const trace = JSON.parse(
  readFileSync(join(root, "trace-v2/otlp/inc-1042-refund.trace.json"), "utf8"),
) as Trace;

type CheckMethod = (...args: unknown[]) => unknown;

describe("effect DSL conformance", () => {
  it("has cases", () => {
    expect(fixture.cases.length).toBeGreaterThan(0);
  });

  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s emits the fixture spec", (_, c) => {
    const chain = attestExpect(trace);
    const snapshot = c.world === null ? null : (fixture.worlds[c.world] ?? null);
    const checks = chain.world(snapshot) as unknown as Record<string, CheckMethod>;
    const method = checks[c.typescript.method];
    expect(typeof method).toBe("function");

    method.apply(checks, c.typescript.args);

    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe("effect");
    expect(chain.assertions[0].spec).toStrictEqual({ ...c.spec, world: snapshot });
  });

  it("count without a bound throws", () => {
    expect(() => attestExpect(trace).world(null).count("refunds", { where: { order_id: "O-1" } })).toThrow(
      "count needs equals, atMost or atLeast",
    );
  });

  it("count with equals and another bound throws", () => {
    expect(() => attestExpect(trace).world(null).count("refunds", { equals: 1, atMost: 2 })).toThrow(
      "equals excludes atMost and atLeast",
    );
  });

  it("unique without fields throws", () => {
    expect(() => attestExpect(trace).world(null).unique("refunds", { by: [] })).toThrow("at least one field");
  });
});
