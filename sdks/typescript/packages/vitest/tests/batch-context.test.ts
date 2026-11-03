/**
 * The running test's identity reaches evaluate_batch as its `context` param.
 * The engine client is mocked; the test under which the assertions run is this
 * file's own Vitest task.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { AttestEngineFixture } from "../src/fixtures.js";
import { ROOT_ENV, batchContextOf, resolveTest, testIdOf } from "../src/test-state.js";

const evaluateBatch = vi.fn(async () => ({
  results: [] as unknown[],
  total_cost: 0,
  total_duration_ms: 0,
}));

vi.mock("@attest-ai/core", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@attest-ai/core")>();
  class MockEngineManager {
    start = vi.fn(async () => ({
      compatible: true,
      engine_version: "1.0.0",
      capabilities: ["layers_1_4"],
    }));
    stop = vi.fn().mockResolvedValue(undefined);
  }
  class MockAttestClient {
    evaluateBatch = evaluateBatch;
  }
  return { ...actual, EngineManager: MockEngineManager, AttestClient: MockAttestClient };
});

const chain = {
  trace: { trace_id: "trc_ctx", output: {}, steps: [] },
  assertions: [{ assertion_id: "a1", type: "schema", spec: {} }],
};

afterEach(() => {
  delete process.env[ROOT_ENV];
  evaluateBatch.mockClear();
});

describe("evaluate_batch context", () => {
  it("is sent with the id and file of the running test", async () => {
    const fixture = new AttestEngineFixture();
    await fixture.start();

    await fixture.inspect(chain as never);

    const options = evaluateBatch.mock.calls[0]![2] as { context: { test_id: string; file: string } };
    const test = await resolveTest(undefined);
    expect(test).toBeDefined();
    expect(options.context.test_id).toBe(testIdOf(test!));
    expect(options.context.test_id).toContain("is sent with the id and file of the running test");
    expect(options.context.file).toMatch(/batch-context\.test\.ts$/);
    await fixture.stop();
  });

  it("names the file relative to the Vitest root the reporter publishes", async () => {
    const test = (await resolveTest(undefined))!;
    process.env[ROOT_ENV] = test.file.filepath!.replace(/\/tests\/batch-context\.test\.ts$/, "");

    expect(batchContextOf(test).file).toBe("tests/batch-context.test.ts");
  });

  it("carries the task's start line when Vitest records locations", () => {
    const task = {
      name: "adds up",
      suite: undefined,
      file: { name: "tests/a.test.ts", filepath: undefined },
      location: { line: 12, column: 3 },
    };

    expect(batchContextOf(task as never)).toEqual({
      test_id: "tests/a.test.ts > adds up",
      file: "tests/a.test.ts",
      line: 12,
    });
  });

  it("omits the line when the task has no location", () => {
    const task = { name: "adds up", suite: undefined, file: { name: "a.test.ts" } };

    expect(batchContextOf(task as never)).not.toHaveProperty("line");
  });
});
