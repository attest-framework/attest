import { describe, it, expect } from "vitest";
import { traceId, assertionId, agentId } from "../../packages/core/src/proto/types.js";

describe("Branded types", () => {
  it("traceId creates a branded string", () => {
    const id = traceId("trc_abc123");
    expect(id).toBe("trc_abc123");
    // At runtime it's still a string
    expect(typeof id).toBe("string");
  });

  it("assertionId creates a branded string", () => {
    const id = assertionId("assert_xyz");
    expect(id).toBe("assert_xyz");
    expect(typeof id).toBe("string");
  });

  it("agentId creates a branded string", () => {
    const id = agentId("my-agent");
    expect(id).toBe("my-agent");
    expect(typeof id).toBe("string");
  });
});
