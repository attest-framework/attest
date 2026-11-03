// Run by replay-e2e.test.ts in a vitest subprocess: useWorld and useReplay in one test.
import { expect, it } from "vitest";
import { useReplay, useWorld } from "../../src/index.js";

const fixturesDir = process.env["REPLAY_FIXTURES"];
const worldFile = process.env["WORLD_FILE"] ?? "";
const refund = { order_id: "O-1042", amount_cents: 4999 };

async function post(key: string, timeoutMs: number): Promise<number | null> {
  try {
    const res = await fetch(`${process.env["PAYMENTS_BASE_URL"]}/refunds`, {
      method: "POST",
      headers: { "content-type": "application/json", "idempotency-key": key },
      body: JSON.stringify(refund),
      signal: AbortSignal.timeout(timeoutMs),
    });
    return res.status;
  } catch {
    return null; // the first refund commits and its response is held
  }
}

it("composes a world with a replay", async () => {
  const world = await useWorld(worldFile);
  await useReplay("inc-1042", { fixturesDir });
  const res = await fetch(`${process.env["OPENAI_BASE_URL"]}/chat/completions`, {
    method: "POST",
    headers: { "content-type": "application/json", authorization: "Bearer unused" },
    body: JSON.stringify({
      model: "gpt-test",
      messages: [
        { role: "system", content: process.env["REPLAY_SYSTEM"] ?? "You refund once." },
        { role: "user", content: "refund order O-7" },
      ],
    }),
  });
  expect(res.ok).toBe(true);

  expect(await post("k", 300)).toBeNull();
  expect(await post("k", 5000)).toBe(200);

  expect(((await world.state())["state"] as Record<string, unknown[]>)["refunds"]).toHaveLength(1);
});

it("has no world variable once the previous test ended", () => {
  expect(process.env["PAYMENTS_BASE_URL"]).toBeUndefined();
});
