// Run by replay-e2e.test.ts in a vitest subprocess: a current-agent replay test under `it.fails`.
import { it } from "vitest";
import { useReplay } from "../../src/index.js";

const fixturesDir = process.env["REPLAY_FIXTURES"];

it.fails("incident reproduces under it.fails", async () => {
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
  throw new Error("double refund reproduced");
});
