import { afterEach, describe, expect, it } from "vitest";
import { resolveTimeoutMs } from "../../packages/core/src/client.js";

describe("resolveTimeoutMs", () => {
  const saved = process.env["ATTEST_ENGINE_TIMEOUT"];

  afterEach(() => {
    if (saved === undefined) delete process.env["ATTEST_ENGINE_TIMEOUT"];
    else process.env["ATTEST_ENGINE_TIMEOUT"] = saved;
  });

  it("reads ATTEST_ENGINE_TIMEOUT as seconds", () => {
    process.env["ATTEST_ENGINE_TIMEOUT"] = "45";
    expect(resolveTimeoutMs()).toBe(45_000);
  });

  it("accepts fractional seconds", () => {
    process.env["ATTEST_ENGINE_TIMEOUT"] = "0.3";
    expect(resolveTimeoutMs()).toBe(300);
  });

  it("accepts the 3600 second ceiling", () => {
    process.env["ATTEST_ENGINE_TIMEOUT"] = "3600";
    expect(resolveTimeoutMs()).toBe(3_600_000);
  });

  it("refuses a legacy millisecond value naming the unit change", () => {
    process.env["ATTEST_ENGINE_TIMEOUT"] = "30000";
    expect(() => resolveTimeoutMs()).toThrow("ATTEST_ENGINE_TIMEOUT is in seconds since 1.1.0");
  });

  it("rejects non-positive and non-numeric values", () => {
    for (const bad of ["0", "-1", "abc"]) {
      process.env["ATTEST_ENGINE_TIMEOUT"] = bad;
      expect(() => resolveTimeoutMs()).toThrow(/positive number of seconds/);
    }
  });

  it("defaults to 30 seconds when unset", () => {
    delete process.env["ATTEST_ENGINE_TIMEOUT"];
    expect(resolveTimeoutMs()).toBe(30_000);
  });
});
