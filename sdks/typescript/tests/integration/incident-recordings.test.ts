/**
 * Recorded refund-desk incidents and fixed runs (protocol-tests/incidents/cases.json).
 *
 * Each recording's applicable contracts are built with the DSL, evaluated through the built
 * engine in one batch, and every row must get its expected status, in order; hard_fail rows
 * must also match their explanation. The incidents must fail on exactly the contracts they
 * broke, and the fixed runs must pass the rest, so a false pass, a wrong contract firing or a
 * misrouted verdict fails here. The Python SDK checks the same file.
 */
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  AttestClient,
  EngineManager,
  attestExpect,
  loadFixture,
  related,
} from "../../packages/core/src/index.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

interface Call {
  method: string;
  args: unknown[];
}

interface Contract {
  id: string;
  effect?: boolean;
  typescript: Call;
}

interface Recording {
  trace?: string;
  fixture?: string;
}

interface ExpectedRow {
  contract: string;
  status: string;
  explanation?: string;
}

interface Cases {
  recordings: Record<string, Recording>;
  contracts: Contract[];
  expected: Record<string, ExpectedRow[]>;
}

const here = dirname(fileURLToPath(import.meta.url));
const protocolTests = resolve(here, "../../../../protocol-tests");
const cases = JSON.parse(readFileSync(join(protocolTests, "incidents", "cases.json"), "utf8")) as Cases;
const contracts = new Map(cases.contracts.map((c) => [c.id, c]));

type Method = (...args: unknown[]) => unknown;

function decode(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(decode);
  if (typeof value !== "object" || value === null) return value;
  const record = value as Record<string, unknown>;
  if ("$related" in record) {
    const ref = record.$related as { resource: string; field: string; on: string };
    return related(ref.resource, ref.field, { on: ref.on });
  }
  return Object.fromEntries(Object.entries(record).map(([k, v]) => [k, decode(v)]));
}

function build(recording: Recording, ids: string[]) {
  let trace: Trace;
  let world: Record<string, unknown> | null = null;
  if (recording.trace !== undefined) {
    trace = JSON.parse(readFileSync(join(protocolTests, recording.trace), "utf8")) as Trace;
  } else {
    const fixture = loadFixture(join(protocolTests, recording.fixture as string));
    trace = fixture.trace as Trace;
    world = fixture.world;
  }
  const chain = attestExpect(trace);
  for (const id of ids) {
    const contract = contracts.get(id);
    if (contract === undefined) throw new Error(`unknown contract ${id}`);
    const target = (contract.effect ? chain.world(world) : chain) as unknown as Record<string, Method>;
    const call = contract.typescript;
    target[call.method].apply(target, call.args.map(decode));
  }
  return { trace, chain };
}

describe("recorded refund-desk incidents and fixed runs", () => {
  let engine: EngineManager;
  let client: AttestClient;

  beforeAll(async () => {
    engine = new EngineManager({ enginePath: resolve(here, "../../../../bin/attest-engine") });
    const init = await engine.start();
    expect(init.compatible).toBe(true);
    client = new AttestClient(engine);
  });

  afterAll(async () => {
    await engine?.stop();
  });

  for (const name of Object.keys(cases.expected).sort()) {
    it(`${name} gets every expected contract status`, async () => {
      const expected = cases.expected[name];
      const { trace, chain } = build(
        cases.recordings[name],
        expected.map((row) => row.contract),
      );

      const result = await client.evaluateBatch(trace, chain.assertions);

      expect(result.results).toHaveLength(expected.length);
      const got = expected.map((want, i) => {
        const row = result.results[i];
        return row.status === "hard_fail"
          ? { contract: want.contract, status: row.status, explanation: row.explanation }
          : { contract: want.contract, status: row.status };
      });
      expect(got).toEqual(expected);
    });
  }
});
