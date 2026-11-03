import { chmodSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it, vi } from "vitest";

// Fake engines stand in for attest-engine; skip its version check.
vi.mock("../../packages/core/src/engine-manager.js", () => ({
  resolveEngine: async (path?: string) => path ?? "",
}));

const { CaptureError, record } = await import("../../packages/core/src/capture.js");

function fakeEngine(script: string): string {
  const path = join(mkdtempSync(join(tmpdir(), "attest-fake-engine-")), "attest-engine");
  writeFileSync(path, `#!/bin/sh\n${script}\n`);
  chmodSync(path, 0o755);
  return path;
}

describe("record() with a misbehaving engine", () => {
  it("throws CaptureError with the engine's stderr when the ready line is not JSON", async () => {
    const engine = fakeEngine("echo 'not json'; cat >/dev/null; echo 'old engine' >&2");

    await expect(record("x", () => undefined, { enginePath: engine })).rejects.toThrow(
      /did not start \(exit 0: old engine\)/,
    );
  });

  it("names the signal when the engine is killed before writing a fixture", async () => {
    const engine = fakeEngine(`echo '{"env":{}}'; kill -9 $$`);

    await expect(record("x", () => undefined, { enginePath: engine })).rejects.toThrow(/killed by SIGKILL/);
  });

  it("refuses a second capture while one is running", async () => {
    const engine = fakeEngine(`echo '{"env":{}}'; cat >/dev/null; echo '{"fixture":"/f","verified":false}'; exit 1`);
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => {
      release = r;
    });

    const first = record("a", () => gate, { enginePath: engine });
    await new Promise((r) => setTimeout(r, 50));
    const second = record("b", () => undefined, { enginePath: engine });

    await expect(second).rejects.toThrow(/already running/);
    release();
    const err = await first.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CaptureError);
    expect((err as InstanceType<typeof CaptureError>).message).toMatch(/not verified; no test drafted/);
  });
});
