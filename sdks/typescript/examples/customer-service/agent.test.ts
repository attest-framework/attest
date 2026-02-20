/**
 * Customer Service Agent — TypeScript SDK example.
 *
 * Demonstrates all 8 assertion layers with a realistic customer service agent.
 * Run: pnpm test
 */
import { describe, it, expect } from "vitest";
import {
  Agent,
  TraceBuilder,
  AgentResult,
  TraceTree,
  attestExpect,
  delegate,
  STEP_AGENT_CALL,
} from "@attest-ai/core";

// -- Define the customer service agent --

const customerService = new Agent(
  "customer-service",
  async (builder: TraceBuilder, args: Record<string, unknown>) => {
    const userMessage = String(args.user_message ?? "");

    // Step 1: Classify intent
    builder.addLlmCall("gpt-4.1-mini", {
      args: { model: "gpt-4.1-mini", messages: [{ role: "user", content: userMessage }] },
      result: { intent: "refund", confidence: 0.95 },
    });

    // Step 2: Look up customer
    builder.addToolCall("lookup_customer", {
      args: { query: userMessage },
      result: {
        customer_id: "CUST-001",
        name: "Jane Smith",
        tier: "gold",
        recent_orders: [{ id: "ORD-5678", amount: 129.99 }],
      },
    });

    // Step 3: Delegate to refund specialist
    await delegate("refund-specialist", (specialist) => {
      specialist.setInput({ order_id: "ORD-5678", reason: userMessage });
      specialist.addToolCall("check_eligibility", {
        args: { order_id: "ORD-5678" },
        result: { eligible: true, max_amount: 129.99 },
      });
      specialist.addToolCall("process_refund", {
        args: { order_id: "ORD-5678", amount: 129.99 },
        result: { refund_id: "RFD-999", status: "approved" },
      });
      specialist.setOutput({ message: "Refund RFD-999 approved for $129.99" });
      specialist.setMetadata({ total_tokens: 80, cost_usd: 0.002, latency_ms: 800 });
    });

    // Step 4: Compose final response
    builder.addLlmCall("gpt-4.1", {
      args: { model: "gpt-4.1" },
      result: {
        completion:
          "Hi Jane, your refund of $129.99 for order ORD-5678 has been approved. " +
          "Refund ID: RFD-999. Funds arrive within 3 business days.",
      },
    });

    builder.setMetadata({ total_tokens: 200, cost_usd: 0.005, latency_ms: 1500, model: "gpt-4.1" });

    return {
      message:
        "Hi Jane, your refund of $129.99 for order ORD-5678 has been approved. " +
        "Refund ID: RFD-999. Funds arrive within 3 business days.",
      structured: {
        intent: "refund",
        refund_id: "RFD-999",
        amount: 129.99,
        status: "approved",
      },
    };
  },
);

// -- Tests: Layers 1-4 --

describe("Customer Service Agent — Layers 1-4", () => {
  it("validates schema, constraints, trace, and content", async () => {
    const result = await customerService.arun({ user_message: "I want a refund" });

    const chain = attestExpect(result)
      // L1: Schema
      .outputMatchesSchema({
        type: "object",
        properties: {
          intent: { type: "string" },
          refund_id: { type: "string" },
          amount: { type: "number" },
        },
        required: ["intent", "refund_id"],
      })
      // L2: Constraints
      .costUnder(0.05)
      .latencyUnder(5000)
      .tokensUnder(500)
      // L3: Trace
      .toolsCalledInOrder(["lookup_customer", "check_eligibility", "process_refund"])
      .requiredTools(["lookup_customer"])
      .forbiddenTools(["delete_customer", "admin_override"])
      // L4: Content
      .outputContains("refund")
      .outputContains("RFD-999")
      .outputNotContains("error");

    expect(chain.assertions.length).toBeGreaterThanOrEqual(10);
  });
});

// -- Tests: Layers 5-6 --

describe("Customer Service Agent — Layers 5-6", () => {
  it("checks semantic similarity and LLM judge", async () => {
    const result = await customerService.arun({ user_message: "I want a refund" });

    const chain = attestExpect(result)
      // L5: Semantic similarity
      .outputSimilarTo("Your refund has been processed and money will be returned.", {
        threshold: 0.7,
      })
      // L6: LLM Judge
      .passesJudge("Does the response address the customer by name and include refund details?", {
        threshold: 0.8,
      })
      .passesJudge("Is the tone professional and empathetic?", {
        threshold: 0.7,
      });

    expect(chain.assertions).toHaveLength(3);
  });
});

// -- Tests: Layers 7-8 --

describe("Customer Service Agent — Layers 7-8", () => {
  it("validates multi-agent delegation chain", async () => {
    const result = await customerService.arun({ user_message: "I want a refund" });

    const chain = attestExpect(result)
      // L8: Multi-agent assertions
      .agentCalled("refund-specialist")
      .delegationDepth(2)
      .followsTransitions([["customer-service", "refund-specialist"]])
      .aggregateCostUnder(0.10)
      .aggregateTokensUnder(1000);

    expect(chain.assertions).toHaveLength(5);
  });

  it("analyzes trace tree structure", async () => {
    const result = await customerService.arun({ user_message: "I want a refund" });

    const tree = new TraceTree(result.trace);

    expect(tree.agents).toContain("customer-service");
    expect(tree.agents).toContain("refund-specialist");
    expect(tree.depth).toBe(1);
    expect(tree.delegations).toEqual([["customer-service", "refund-specialist"]]);
    expect(tree.aggregateTokens).toBeGreaterThan(0);
    expect(tree.aggregateCost).toBeGreaterThan(0);
    expect(tree.allToolCalls().length).toBeGreaterThan(0);
  });
});
