import { describe, expect, it } from "vitest";
import { attestExpect } from "../../packages/core/src/index.js";
import { related } from "../../packages/core/src/expect.js";
import type { Trace } from "../../packages/core/src/proto/types.js";

const trace = { trace_id: "t", output: {}, steps: [] } as unknown as Trace;
const world = () => attestExpect(trace).world({ world_version: 1 });
const orders = related("orders", "amount_cents", { on: "order_id" });

describe("sum", () => {
  it("builds the engine spec for a related bound", () => {
    const chain = world().sum("refunds", "amount_cents", { atMost: orders, where: { status: "paid" } });
    expect(chain.assertions[0]!.spec).toMatchObject({
      check: "sum",
      resource: "refunds",
      field: "amount_cents",
      at_most: { related: { resource: "orders", field: "amount_cents", on: "order_id" } },
      where: { status: "paid" },
    });
  });

  it("builds the engine spec for a numeric bound", () => {
    const chain = world().sum("refunds", "amount_cents", { atMost: 15000 });
    expect(chain.assertions[0]!.spec).toMatchObject({ check: "sum", at_most: 15000 });
  });

  it.each([
    ["a string bound", () => world().sum("refunds", "amount_cents", { atMost: "5" as never }), /related/],
    ["NaN", () => world().sum("refunds", "amount_cents", { atMost: Number.NaN }), /finite/],
    ["a missing bound", () => world().sum("refunds", "amount_cents", {} as never), /related/],
    ["a bare related body", () => world().sum("refunds", "amount_cents", { atMost: { resource: "o" } as never }), /related/],
    ["an empty field", () => world().sum("refunds", "", { atMost: 1 }), /field/],
  ])("rejects %s at build time", (_name, build, message) => {
    expect(build).toThrow(message);
  });
});

describe("matches", () => {
  it("builds the engine spec", () => {
    const rel = related("orders", "customer_id", { on: "order_id" });
    const chain = world().matches("notifications", "customer_id", rel);
    expect(chain.assertions[0]!.spec).toMatchObject({
      check: "matches",
      resource: "notifications",
      field: "customer_id",
      related: { resource: "orders", field: "customer_id", on: "order_id" },
    });
  });

  it("rejects a plain value instead of related(...)", () => {
    expect(() => world().matches("notifications", "customer_id", "orders" as never)).toThrow(/related/);
  });
});

describe("related", () => {
  it.each([
    ["resource", () => related("", "f", { on: "k" })],
    ["field", () => related("r", "", { on: "k" })],
    ["on", () => related("r", "f", { on: "" })],
    ["on (missing)", () => related("r", "f", {} as never)],
  ])("rejects an empty %s", (_name, build) => {
    expect(build).toThrow(/non-empty string/);
  });
});
