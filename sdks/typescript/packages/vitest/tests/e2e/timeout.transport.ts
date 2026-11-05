// Run by transport-routing.test.ts in a vitest subprocess against the real engine.
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "../../src/index.js";

const trace = new TraceBuilder("refund-agent")
  .setOutput({ message: "Your refund has been issued." })
  .build();

// A local judge that holds its first answer past the forced request timeout.
let judge: Server;
let calls = 0;

beforeAll(async () => {
  judge = createServer((req, res) => {
    req.resume();
    req.on("end", () => {
      calls += 1;
      const reply = (): void => {
        res.setHeader("content-type", "application/json");
        res.end(
          JSON.stringify({
            model: "fake-model",
            choices: [{ message: { content: '{"score": 0.9, "explanation": "fine"}' } }],
            usage: { prompt_tokens: 10, completion_tokens: 5 },
          }),
        );
      };
      if (calls === 1) setTimeout(reply, 1_500);
      else reply();
    });
  });
  await new Promise<void>((done) => judge.listen(0, "127.0.0.1", done));
  const port = (judge.address() as AddressInfo).port;
  process.env["ATTEST_JUDGE_PROVIDER"] = "openai";
  process.env["ATTEST_JUDGE_BASE_URL"] = `http://127.0.0.1:${port}/v1`;
  process.env["ATTEST_JUDGE_API_KEY"] = "fake";
  process.env["ATTEST_JUDGE_MODEL"] = "fake-model";
});

afterAll(async () => {
  judge.closeAllConnections();
  await new Promise<void>((done) => judge.close(() => done()));
});

it("T1 times out", async (context) => {
  process.env["ATTEST_ENGINE_TIMEOUT"] = "0.3";
  try {
    await evaluate(attestExpect(trace).passesJudge("Is it ok?"), { context });
  } finally {
    delete process.env["ATTEST_ENGINE_TIMEOUT"];
  }
});

it("T2 fails on its own verdict", async (context) => {
  await evaluate(attestExpect(trace).outputContains("absent-in-t2"), { context });
});
