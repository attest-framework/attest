import { describe, expect, it } from "vitest";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  PARITY_DIR,
  UPDATE_ENV,
  adapterParityProblems,
  collectSurface,
  compareSurface,
  serializeSurface,
  type Surface,
} from "../support/parity-surface.js";
import { ENGINE_PATH, adapterStatuses } from "../support/adapter-conformance.js";

const empty: Surface = {
  adapters: [],
  exports: [],
  expect_chain: [],
  fixture_methods: [],
  init_flags: [],
  plugins: [],
  simulation: [],
  test_runner: [],
};

describe("compareSurface", () => {
  it("reports no drift for identical surfaces", () => {
    const s = { ...empty, exports: ["a", "b"] };
    expect(compareSurface(s, { ...s })).toEqual([]);
  });

  it("reports a symbol added to the live surface", () => {
    const committed = { ...empty, simulation: ["scenario"] };
    const live = { ...empty, simulation: ["scenario", "faultInject"] };
    expect(compareSurface(committed, live)).toEqual(["added simulation.faultInject"]);
  });

  it("reports a symbol removed from the live surface", () => {
    const committed = { ...empty, exports: ["a", "b"] };
    const live = { ...empty, exports: ["a"] };
    expect(compareSurface(committed, live)).toEqual(["removed exports.b"]);
  });
});

describe("typescript parity snapshot", () => {
  it("matches the live public surface", () => {
    const live = collectSurface();
    const file = join(PARITY_DIR, "typescript.json");
    if (process.env[UPDATE_ENV] === "1") {
      mkdirSync(PARITY_DIR, { recursive: true });
      writeFileSync(file, serializeSurface(live));
    }
    expect(existsSync(file), `missing ${file}; regenerate with ${UPDATE_ENV}=1`).toBe(true);
    const committed = JSON.parse(readFileSync(file, "utf8")) as Surface;
    const drift = compareSurface(committed, live);
    expect(drift, `snapshot drift; regenerate with ${UPDATE_ENV}=1 pnpm -r test\n${drift.join("\n")}`).toEqual([]);
  });
});

describe("adapter parity", () => {
  it("flags an adapter matched by name only", () => {
    const problems = adapterParityProblems(["FooAdapter", "BarAdapter", "BazAdapter"], {
      FooAdapter: "passing",
      BarAdapter: "none",
      BazAdapter: "failing",
    });

    expect(problems).toEqual([
      "adapter BarAdapter: matches by name only; add conformance cases under sdks/parity/adapters",
      "adapter BazAdapter: corpus cases fail; the trace differs from the golden",
    ]);
  });

  it("counts every exported adapter as a match only when its corpus cases pass", async () => {
    process.env.ATTEST_ENGINE_PATH = ENGINE_PATH;

    const problems = adapterParityProblems(collectSurface().adapters, await adapterStatuses());

    expect(problems).toEqual([]);
  });
});
