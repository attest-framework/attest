import { describe, expect, it } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";

// engine/internal/capabilities/data.json is the single source of the adapter lists in the README
// and the guides. A file added to or removed from the adapters directory fails here until the
// source lists it with a maturity tier.
const repoRoot = path.resolve(import.meta.dirname, "../../../..");
const adaptersDir = path.join(repoRoot, "sdks/typescript/packages/core/src/adapters");
const helperFiles = new Set(["index", "provider-trace"]);
const tiers = new Set(["stable", "beta", "experimental"]);

interface ListedAdapter {
  name: string;
  module: string;
  maturity: string;
}

const data = JSON.parse(
  fs.readFileSync(path.join(repoRoot, "engine/internal/capabilities/data.json"), "utf8"),
) as { adapters: { typescript: ListedAdapter[] } };

describe("capabilities adapter list", () => {
  it("matches the TypeScript adapter modules", () => {
    const onDisk = fs
      .readdirSync(adaptersDir)
      .filter((f) => f.endsWith(".ts"))
      .map((f) => f.slice(0, -3))
      .filter((stem) => !helperFiles.has(stem))
      .sort();

    const listed = data.adapters.typescript.map((a) => a.module).sort();

    expect(listed).toEqual(onDisk);
  });

  it("gives every adapter a known maturity tier", () => {
    for (const adapter of data.adapters.typescript) {
      expect(tiers.has(adapter.maturity), adapter.module).toBe(true);
    }
  });
});

describe("npm package licence", () => {
  const rootLicense = fs.readFileSync(path.join(repoRoot, "LICENSE"), "utf8");

  it.each(["core", "vitest"])("declares Apache-2.0 and ships the LICENSE file (%s)", (pkg) => {
    const dir = path.join(repoRoot, "sdks/typescript/packages", pkg);
    const manifest = JSON.parse(fs.readFileSync(path.join(dir, "package.json"), "utf8")) as {
      license: string;
    };

    expect(manifest.license).toBe("Apache-2.0");
    expect(fs.readFileSync(path.join(dir, "LICENSE"), "utf8")).toBe(rootLicense);
  });
});
