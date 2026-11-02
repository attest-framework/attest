// Run by attest-trials.test.ts in a vitest subprocess against the real engine.
import { describe, it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate, trials } from "../../src/index.js";

function reply(message: string) {
  return new TraceBuilder("refund-agent")
    .addToolCall("lookup_order", { args: { order_id: "ORD-1" } })
    .setOutput({ message })
    .setMetadata({ cost_usd: 0.001 })
    .build();
}

describe("refund desk", () => {
  it("refunds once", async (context) => {
    await trials(
      async (trial) => {
        // The demo model server fails a seeded fraction of chat requests.
        if (trial === 2) throw new Error("model server returned HTTP 500");
        const message = trial === 3 ? "Here is store credit." : "Your refund has been issued.";
        await evaluate(attestExpect(reply(message)).outputContains("refund").requiredTools(["lookup_order"]));
      },
      { n: 4, context, budget: { maxJudgeCalls: 2, maxCostUsd: 1 } },
    );
  });

  it("judge without calls left", async (context) => {
    await trials(
      async () => {
        await evaluate(attestExpect(reply("Your refund has been issued.")).passesJudge("Is it polite?"));
      },
      { n: 2, context, budget: { maxJudgeCalls: 0 } },
    );
  });
});
