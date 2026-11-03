/**
 * `attest init --skills --json` against the engine built by `make engine` (or the engine at
 * ATTEST_ENGINE_PATH), validated against proto/attest/v1/cli/init.schema.json.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { main } from "../../packages/core/src/cli.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const validate = new Ajv2020({ strict: false }).compile(
  JSON.parse(
    fs.readFileSync(join(root, "proto", "attest", "v1", "cli", "init.schema.json"), "utf8"),
  ) as Record<string, unknown>,
);
// ATTEST_ENGINE_PATH selects a branch-built engine; otherwise the `make engine` binary.
const enginePath = process.env["ATTEST_ENGINE_PATH"] || join(root, "bin", "attest-engine");
const SCAFFOLD = [".github/workflows/attest.yml", "attest.policy.yaml", "tests/my-agent.test.ts"];

describe("attest init --skills --json with the built engine", () => {
  let project: string;
  let originalCwd: string;
  let stdout: string[];
  const saved: Record<string, string | undefined> = {};

  beforeEach(() => {
    originalCwd = process.cwd();
    project = fs.mkdtempSync(join(os.tmpdir(), "attest-init-skills-"));
    process.chdir(project);
    for (const key of ["ATTEST_ENGINE_PATH", "ATTEST_ENGINE_NO_DOWNLOAD"]) saved[key] = process.env[key];
    process.env["ATTEST_ENGINE_PATH"] = enginePath;
    process.env["ATTEST_ENGINE_NO_DOWNLOAD"] = "1";
    stdout = [];
    vi.spyOn(process.stdout, "write").mockImplementation((chunk) => {
      stdout.push(String(chunk));
      return true;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    process.chdir(originalCwd);
    fs.rmSync(project, { recursive: true, force: true });
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });

  async function initJson(...args: string[]): Promise<Record<string, string[]>> {
    stdout.length = 0;
    await expect(main(["init", ...args, "--json"])).resolves.toBe(0);
    expect(stdout).toHaveLength(1);
    const doc = JSON.parse(stdout[0]!) as Record<string, string[]>;
    expect(validate(doc), JSON.stringify(validate.errors)).toBe(true);
    return doc;
  }

  it("reports the skills and the scaffold as created", async (ctx) => {
    if (!fs.existsSync(enginePath)) {
      ctx.skip();
      return;
    }

    const doc = await initJson("--skills", "--ci", "github");

    for (const rel of SCAFFOLD) expect(doc["created"]).toContain(rel);
    expect(doc["created"]!.filter((rel) => !SCAFFOLD.includes(rel)).length).toBeGreaterThan(0);
    for (const rel of doc["created"]!) expect(fs.existsSync(join(project, rel))).toBe(true);
    expect(doc["updated"]).toEqual([]);
    expect(doc["skipped"]).toEqual([]);
  });

  it("reports everything as skipped on a rerun", async (ctx) => {
    if (!fs.existsSync(enginePath)) {
      ctx.skip();
      return;
    }
    const first = await initJson("--skills");

    const second = await initJson("--skills");

    expect(second).toEqual({ created: [], updated: [], skipped: first["created"] });
  });
});
