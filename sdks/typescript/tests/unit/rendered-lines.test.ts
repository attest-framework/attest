/**
 * The chain lines the engine's contract catalog renders (protocol-tests/contracts/rendered.json,
 * checked against the Go catalog by a drift test) applied to a real `attestExpect` chain.
 */

import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { attestExpect, loadFixture, related } from "../../packages/core/src/index.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const golden = JSON.parse(
  readFileSync(join(root, "protocol-tests", "contracts", "rendered.json"), "utf-8"),
) as {
  cases: {
    statement: string;
    type: string;
    spec: Record<string, unknown>;
    chain_prefix: string;
    typescript: string;
  }[];
};
// A fixture with a world snapshot, as `FIXTURE` is in a generated test.
const FIXTURE = loadFixture(
  join(root, "protocol-tests", "fixture-v1", "valid", "005-world-snapshot.attest.json"),
);

describe("rendered TypeScript contract lines", () => {
  it("covers every catalog method", () => {
    expect(golden.cases.length).toBeGreaterThanOrEqual(13);
  });

  it.each(golden.cases.map((c) => [c.statement, c] as const))("%s", (_statement, c) => {
    const chain = attestExpect(FIXTURE.trace);
    const before = chain.assertions.length;

    // The line is applied to a chain exactly as a generated test pastes it.
    const run = new Function("chain", "FIXTURE", "related", `return chain${c.typescript};`) as (
      chain: unknown,
      fixture: unknown,
      related: unknown,
    ) => { assertions: { type: string; spec: Record<string, unknown> }[] };
    const result = run(chain, FIXTURE, related);

    expect(result.assertions.length).toBe(before + 1);
    const recorded = result.assertions[result.assertions.length - 1]!;
    const spec = { ...recorded.spec };
    if (recorded.type === "effect") {
      // An effect spec embeds the world it checks; it must be the fixture's.
      expect(spec["world"]).toEqual(FIXTURE.world);
      delete spec["world"];
    }
    expect(recorded.type).toBe(c.type);
    expect(spec).toEqual(c.spec);
    if (c.chain_prefix !== "") expect(c.typescript.startsWith(c.chain_prefix)).toBe(true);
  });
});
