// Run by replay-e2e.test.ts in a vitest subprocess: no keys, ATTEST_EGRESS=block.
import { expect, it } from "vitest";
import { useReplay } from "../../src/index.js";

const fixturesDir = process.env["REPLAY_FIXTURES"];

it("replays inc-1042", async () => {
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
  if (!res.ok) throw new Error(`model call failed: ${res.status}`);
  const body = (await res.json()) as { choices: Array<{ message: { content: string } }> };
  expect(body.choices[0]?.message.content).toBe("done");
});

it("blocks direct provider connections", async () => {
  await useReplay("inc-1042", { fixturesDir });
  const err = (await fetch("https://api.openai.com/v1/models").catch((e: unknown) => e)) as Error;
  expect(String(err.cause)).toMatch(/egress guard: connection to model provider api\.openai\.com blocked/);
});
