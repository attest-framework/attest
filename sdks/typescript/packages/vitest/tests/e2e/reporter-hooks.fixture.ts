import { afterAll, beforeAll, describe, expect, it } from "vitest";

const hook = process.env["ATTEST_TEST_HOOK"];
const scope = process.env["ATTEST_TEST_HOOK_SCOPE"];
const hookError = process.env["ATTEST_TEST_HOOK_ERROR"] === "AggregateError"
  ? new AggregateError([], "empty hook rejection")
  : new Error("reporter hook regression");

function registerHooks(): void {
  beforeAll(() => {
    if (hook === "beforeAll") throw hookError;
    if (hook === "cleanup") return () => { throw hookError; };
  });
  afterAll(() => {
    if (hook === "afterAll" || hook === "test-and-afterAll") throw hookError;
  });
}

if (scope === "module") registerHooks();

describe("affected suite", () => {
  if (scope === "suite") registerHooks();

  it("hooked test", () => {
    if (hook === "test" || hook === "test-and-afterAll") throw new Error("ordinary test failure");
    expect(true).toBe(true);
  });

  describe("nested suite", () => {
    it("nested test", () => { expect(true).toBe(true); });
  });
});

describe("unrelated suite", () => {
  it("untouched test", () => { expect(true).toBe(true); });
  it.skip("intentional skip", () => {});
});
