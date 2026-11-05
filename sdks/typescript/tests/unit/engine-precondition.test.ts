import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { assertEngineCurrent } from "../support/engine-precondition.js";

describe("assertEngineCurrent", () => {
  let root: string;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "attest-precondition-"));
    writeFileSync(join(root, "VERSION"), "1.2.3\n");
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  function installEngine(): void {
    mkdirSync(join(root, "bin"));
    writeFileSync(join(root, "bin", "attest-engine"), "");
  }

  it("fails with the make engine remedy when the binary is missing", () => {
    expect(() => assertEngineCurrent(root, () => "1.2.3")).toThrow(/missing.*make engine/);
  });

  it("fails with the make engine remedy when the binary version differs from VERSION", () => {
    installEngine();
    expect(() => assertEngineCurrent(root, () => "1.0.0")).toThrow(
      /stale.*reports version 1\.0\.0, VERSION is 1\.2\.3.*make engine/,
    );
  });

  it("passes when the binary version matches VERSION", () => {
    installEngine();
    expect(() => assertEngineCurrent(root, () => "1.2.3")).not.toThrow();
  });
});
