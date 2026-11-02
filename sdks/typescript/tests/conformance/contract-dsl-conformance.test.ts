/**
 * Cross-SDK behavior-contract fixtures (protocol-tests/contracts/dsl.json).
 * Each case names a DSL call; the TypeScript chain must emit exactly the
 * fixture's spec. The Python SDK checks the same specs, and the engine
 * evaluates them.
 */
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { ExpectChain, attestExpect } from "../../packages/core/src/expect.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

interface ContractCase {
  name: string;
  typescript: { method: string; args: unknown[] };
  spec: Record<string, unknown>;
}

interface ContractFixture {
  traces: Record<string, string>;
  cases: ContractCase[];
}

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../protocol-tests");
const fixture = JSON.parse(
  readFileSync(join(root, "contracts", "dsl.json"), "utf8"),
) as ContractFixture;

function loadTrace(name: string): Trace {
  return JSON.parse(readFileSync(join(root, fixture.traces[name]), "utf8")) as Trace;
}

type DslMethod = (...args: unknown[]) => ExpectChain;

describe("behavior-contract DSL conformance", () => {
  it("has cases", () => {
    expect(fixture.cases.length).toBeGreaterThan(0);
  });

  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s emits the fixture spec", (_, c) => {
    const chain = attestExpect(loadTrace("inc-1042-py"));
    const method = (chain as unknown as Record<string, DslMethod>)[c.typescript.method];
    expect(typeof method).toBe("function");

    method.apply(chain, c.typescript.args);

    expect(chain.assertions).toHaveLength(1);
    expect(chain.assertions[0].type).toBe("trace");
    expect(chain.assertions[0].spec).toStrictEqual(c.spec);
  });

  it("toolCardinality without bounds throws", () => {
    const chain = attestExpect(loadTrace("inc-1042-py"));
    expect(() => chain.toolCardinality("issue_refund", { per: "order_id" })).toThrow(
      "atMost, atLeast or both",
    );
  });
});
