import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

const run = promisify(execFile);
const coreRoot = path.resolve(import.meta.dirname, "../../packages/core");

// An install directory whose name holds '#', the character a file: URL treats as a fragment.
let installDir: string;
let scratch: string;

beforeEach(() => {
  scratch = fs.mkdtempSync(path.join(os.tmpdir(), "attest-table-location-"));
  installDir = path.join(scratch, "in#stall", "node_modules", "@attest-ai", "core");
  fs.mkdirSync(installDir, { recursive: true });
  for (const built of ["dist/cjs/index.cjs", "dist/esm/index.js"]) {
    if (!fs.existsSync(path.join(coreRoot, built))) {
      throw new Error(`${built} is missing: run \`pnpm build\` before this test`);
    }
  }
  fs.cpSync(path.join(coreRoot, "dist"), path.join(installDir, "dist"), { recursive: true });
  fs.copyFileSync(path.join(coreRoot, "package.json"), path.join(installDir, "package.json"));
  // Resolves the bundle's runtime dependencies; the table itself must come from installDir.
  fs.symlinkSync(path.join(coreRoot, "node_modules"), path.join(installDir, "node_modules"));
  // A table with no entry for this engine version: finding it fails with "ships no valid engine
  // hash" without any network access, whereas not finding it reports "no embedded engine hash
  // table" (the source-install message).
  fs.writeFileSync(path.join(installDir, "engine-checksums.json"), "{}");
});

afterEach(() => {
  fs.rmSync(scratch, { recursive: true, force: true });
});

// A script file, not `node -e`: eval mode defines a global __dirname that ESM never has.
async function startMessage(file: string, source: string): Promise<string> {
  const script = path.join(scratch, file);
  fs.writeFileSync(script, source);
  const env = { ...process.env, HOME: scratch, PATH: path.join(scratch, "empty") };
  delete env["ATTEST_ENGINE_PATH"];
  delete env["ATTEST_ENGINE_NO_DOWNLOAD"];
  const { stdout } = await run(process.execPath, [script], { cwd: scratch, env });
  return stdout;
}

const startAndPrint = `
  try { await new m.EngineManager().start(); } catch (e) { console.log(e.message); }
`;

describe("embedded engine table location in the built output", () => {
  it("is found by the CJS build when the install path contains '#'", async () => {
    const entry = path.join(installDir, "dist/cjs/index.cjs");
    const message = await startMessage(
      "probe.cjs",
      `const m = require(${JSON.stringify(entry)});\n(async () => {${startAndPrint}})();`,
    );
    expect(message).toContain("ships no valid engine hash");
    expect(message).not.toContain("no embedded engine hash table");
  });

  it("is found by the ESM build when the install path contains '#'", async () => {
    const entry = path.join(installDir, "dist/esm/index.js");
    const message = await startMessage(
      "probe.mjs",
      `const m = await import(${JSON.stringify(pathToFileURL(entry).href)});\n${startAndPrint}`,
    );
    expect(message).toContain("ships no valid engine hash");
    expect(message).not.toContain("no embedded engine hash table");
  });
});
