/**
 * Worlds against the built engine: a scripted HTTP client drives the INC-1042 payments
 * world, then effect assertions read the world's state. Skipped when the engine binary has
 * not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { AssertionIdAssigner, canonicalSpec } from "../../packages/core/src/assertion-id.js";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  AttestClient,
  EngineManager,
  WorldError,
  attestExpect,
  startWorld,
} from "../../packages/core/src/index.js";
import { related } from "../../packages/core/src/expect.js";
import type { WorldSnapshot } from "../../packages/core/src/index.js";
import type { AssertionResult, Trace } from "../../packages/core/src/proto/types.js";

const here = dirname(fileURLToPath(import.meta.url));
const enginePath = resolve(here, "../../../../bin/attest-engine");
const worldFile = join(here, "data", "payments.world.yaml");
const trace = JSON.parse(
  readFileSync(join(here, "../../../../protocol-tests/trace-v2/otlp/inc-1042-refund.trace.json"), "utf8"),
) as Trace;

// The first refund is committed and its response held; the client gives up well before.
const CLIENT_TIMEOUT_MS = 300;
const order1042 = { order_id: "O-1042", amount_cents: 4999 };

/** POST a refund the way the INC-1042 agent does. `null`: the client timed out. */
async function postRefund(baseUrl: string, key?: string): Promise<number | null> {
  try {
    const res = await fetch(`${baseUrl}/refunds`, {
      method: "POST",
      headers: { "content-type": "application/json", ...(key === undefined ? {} : { "idempotency-key": key }) },
      body: JSON.stringify(order1042),
      signal: AbortSignal.timeout(CLIENT_TIMEOUT_MS),
    });
    await res.arrayBuffer();
    return res.status;
  } catch (err) {
    if (err instanceof DOMException && err.name === "TimeoutError") return null;
    throw err;
  }
}

async function doubleRefund(baseUrl: string, key?: string): Promise<void> {
  expect(await postRefund(baseUrl, key)).toBeNull();
  expect([200, 201]).toContain(await postRefund(baseUrl, key));
}

// Stopping the engine can take a few seconds after a client request was aborted, so a test that
// stops two worlds needs more than the default timeout.
describe.skipIf(!existsSync(enginePath))("world (real engine)", { timeout: 30_000 }, () => {
  let engine: EngineManager;
  let client: AttestClient;

  beforeAll(async () => {
    engine = new EngineManager({ enginePath });
    await engine.start();
    client = new AttestClient(engine);
  });

  afterAll(async () => {
    await engine.stop();
  });

  async function check(
    snapshot: WorldSnapshot | null,
    build: (checks: ReturnType<ReturnType<typeof attestExpect>["world"]>) => unknown,
  ): Promise<AssertionResult> {
    const chain = attestExpect(trace);
    build(chain.world(snapshot));
    const result = await client.evaluateBatch(trace, chain.assertions);
    expect(result.results).toHaveLength(1);
    return result.results[0] as AssertionResult;
  }

  it("keeps the effect contract identity through passing and failing worlds and baseline policy", async () => {
    const keyed = await startWorld(worldFile, { enginePath });
    let single: WorldSnapshot;
    try {
      await doubleRefund(keyed.baseUrl, "stable-contract");
      single = await keyed.state();
    } finally {
      await keyed.stop();
    }
    const keyless = await startWorld(worldFile, { enginePath });
    let doubled: WorldSnapshot;
    try {
      await doubleRefund(keyless.baseUrl);
      doubled = await keyless.state();
    } finally {
      await keyless.stop();
    }
    const scopeId = "effect baseline contract";
    const evaluate = async (world: WorldSnapshot) => {
      const chain = attestExpect(trace).world(world).count("refunds", { equals: 1 });
      const source = chain.assertions[0]!;
      const assertion = {
        ...source,
        scope_id: scopeId,
        assertion_id: new AssertionIdAssigner(scopeId).next(source.type, source.spec),
      };
      expect(assertion.spec["world"]).toEqual(world);
      const result = await client.evaluateBatch(trace, [assertion]);
      const row = result.results[0]!;
      const canonical = canonicalSpec(assertion.spec, { scopeId, type: assertion.type, spec: assertion.spec });
      expect(row.spec_hash).toBe(createHash("sha256").update(canonical).digest("hex"));
      return row;
    };
    const baseline = await evaluate(single);
    const current = await evaluate(doubled);
    expect([baseline.status, current.status]).toEqual(["pass", "hard_fail"]);
    expect(current.assertion_id).toBe(baseline.assertion_id);
    expect(current.spec_hash).toBe(baseline.spec_hash);

    const dir = mkdtempSync(join(tmpdir(), "attest-effect-identity-"));
    try {
      const report = (name: string, row: AssertionResult) => {
        const file = join(dir, name);
        writeFileSync(file, JSON.stringify({
          report_version: 2, id_scheme: 2, effect_world_excluded: true, timestamp: "2026-11-07T00:00:00Z",
          summary: { total: 1, passed: row.status === "pass" ? 1 : 0, hard_fail: row.status === "hard_fail" ? 1 : 0, soft_fail: 0, not_evaluated: 0 },
          results: [row], tests: [], total_cost: 0, total_duration_ms: 0,
        }));
        return file;
      };
      const run = (...args: string[]) => spawnSync(enginePath, args, {
        encoding: "utf8", env: { ...process.env, ATTEST_CACHE_DIR: join(dir, "cache") },
      });
      const pin = run("baseline", "pin", "--tag", "effect-good", "--report", report("baseline.json", baseline));
      expect(pin.status, pin.stderr).toBe(0);
      const policy = join(dir, "policy.yaml");
      writeFileSync(policy, "block_on_regression: true\n");
      const evaluated = run("policy", "evaluate", "--policy", policy, "--report", report("current.json", current), "--baseline", "effect-good");
      expect(evaluated.status, evaluated.stderr).toBe(1);
      const output = JSON.parse(evaluated.stdout) as {
        violations: Array<{ rule: string; detail: string }>;
        changed?: unknown[];
      };
      expect(output.violations).toContainEqual(expect.objectContaining({
        rule: "block_on_regression",
        detail: `regressions vs effect-good: ${current.assertion_id}`,
      }));
      expect(output.changed ?? []).toEqual([]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it("count: a double refund without a key fails with the violation text", async () => {
    const world = await startWorld(worldFile, { enginePath });
    let row: AssertionResult;
    try {
      await doubleRefund(world.baseUrl);
      row = await check(await world.state(), (w) =>
        w.count("refunds", { equals: 1, where: { order_id: "O-1042" } }),
      );
    } finally {
      await world.stop();
    }

    expect(row.status).toBe("hard_fail");
    expect(row.explanation).toBe("effect violated: 2 refunds for O-1042 (expected 1)");
  });

  it("count: the same idempotency key on both POSTs passes", async () => {
    const world = await startWorld(worldFile, { enginePath });
    let row: AssertionResult;
    try {
      await doubleRefund(world.baseUrl, "refund-O-1042");
      row = await check(await world.state(), (w) =>
        w.count("refunds", { equals: 1, where: { order_id: "O-1042" } }),
      );
    } finally {
      await world.stop();
    }

    expect(row.status).toBe("pass");
  });

  it("count bounds, unique and idempotent report on the final snapshot", async () => {
    const keyless = await startWorld(worldFile, { enginePath });
    await doubleRefund(keyless.baseUrl);
    const doubled = await keyless.stop();
    const keyed = await startWorld(worldFile, { enginePath });
    await doubleRefund(keyed.baseUrl, "refund-O-1042");
    const single = await keyed.stop();

    expect((await check(doubled, (w) => w.count("refunds", { atLeast: 2 }))).status).toBe("pass");
    expect((await check(doubled, (w) => w.count("refunds", { atMost: 1 }))).status).toBe("hard_fail");
    expect((await check(doubled, (w) => w.unique("refunds", { by: "refund_id" }))).status).toBe("pass");
    const notUnique = await check(doubled, (w) =>
      w.unique("refunds", { by: ["order_id"], where: { order_id: "O-1042" } }),
    );
    expect(notUnique.status).toBe("hard_fail");
    expect(notUnique.explanation).toMatch(/^effect violated:/);
    expect((await check(single, (w) => w.idempotent("refunds"))).status).toBe("pass");
    const noKey = await check(doubled, (w) => w.idempotent("refunds", { where: { order_id: "O-1042" } }));
    expect(noKey.status).toBe("hard_fail");
    expect(noKey.explanation).toMatch(/^effect violated:/);
  });

  it("sum and matches report relational violations from the engine", async () => {
    const snapshot = (refunds: unknown[], notifications: unknown[]): WorldSnapshot => ({
      world_version: 1,
      definition_sha256: "0".repeat(64),
      state: {
        orders: [{ order_id: "O-2077", customer_id: "C-0311", amount_cents: 12600 }],
        refunds,
        notifications,
      },
      faults_fired: [],
      faults_fired_count: 0,
    });
    const bound = related("orders", "amount_cents", { on: "order_id" });
    const sumCheck = (w: Parameters<Parameters<typeof check>[1]>[0]) =>
      w.sum("refunds", "amount_cents", { atMost: bound });
    const refund = (cents: number) => ({ order_id: "O-2077", amount_cents: cents });

    const over = await check(snapshot([refund(1200), refund(12600)], []), sumCheck);
    expect(over.status).toBe("hard_fail");
    expect(over.explanation).toBe(
      "effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600",
    );
    expect((await check(snapshot([refund(1200), refund(11400)], []), sumCheck)).status).toBe("pass");

    const customer = related("orders", "customer_id", { on: "order_id" });
    const matchCheck = (w: Parameters<Parameters<typeof check>[1]>[0]) =>
      w.matches("notifications", "customer_id", customer);
    const misdirected = await check(
      snapshot([], [{ order_id: "O-2077", customer_id: "C-0520" }]),
      matchCheck,
    );
    expect(misdirected.status).toBe("hard_fail");
    expect(misdirected.explanation).toBe(
      "effect violated: notifications customer_id C-0520 for O-2077 does not match orders.customer_id C-0311",
    );
    expect(
      (await check(snapshot([], [{ order_id: "O-2077", customer_id: "C-0311" }]), matchCheck)).status,
    ).toBe("pass");
  });

  it("a null snapshot is not evaluated with reason missing_evidence", async () => {
    const row = await check(null, (w) => w.count("refunds", { equals: 1 }));

    expect(row.status).toBe("not_evaluated");
    expect(row.reason).toBe("missing_evidence");
  });

  it("the session sets and restores its env, serves state and stops idempotently", async () => {
    expect(process.env["PAYMENTS_BASE_URL"]).toBeUndefined();

    const world = await startWorld(worldFile, { enginePath });
    expect(process.env["PAYMENTS_BASE_URL"]).toBe(world.baseUrl);
    expect(world.env).toEqual({ PAYMENTS_BASE_URL: world.baseUrl });
    expect(((await world.state())["state"] as Record<string, unknown[]>)["refunds"]).toEqual([]);
    expect(await postRefund(world.baseUrl)).toBeNull();
    await expect(startWorld(worldFile, { enginePath })).rejects.toThrow("cannot overlap");
    const final = await world.stop();

    expect(process.env["PAYMENTS_BASE_URL"]).toBeUndefined();
    expect(((final["state"] as Record<string, unknown[]>)["refunds"] ?? []).length).toBe(1);
    expect(await world.stop()).toBe(final);
    await expect(world.state()).rejects.toThrow("is stopped");
  });

  it("a malformed world file rejects with the schema error and releases the lock", async () => {
    const bad = join(here, "data", "bad.world.yaml");

    await expect(startWorld(bad, { enginePath })).rejects.toThrow(WorldError);

    const world = await startWorld(worldFile, { enginePath });
    await world.stop();
  });
});
