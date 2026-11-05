import { chmodSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fakeEnginePath = vi.hoisted(() => ({ value: "" }));
vi.mock("../../packages/core/src/engine-manager.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../packages/core/src/engine-manager.js")>()),
  resolveEngine: async () => fakeEnginePath.value,
}));

import { CaptureError, loadFixture, renderVitest, replayRun } from "../../packages/core/src/index.js";
import { captureWorldPath } from "../../packages/core/src/cli.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const validDir = join(root, "protocol-tests", "fixture-v1", "valid");
const commandFixture = join(validDir, "001-command-capture-expect-fail.attest.json");
const otlpFixture = join(validDir, "002-otlp-file-trace-only.attest.json");

// A stand-in engine: records how it was called and writes a trace file, as
// `attest-engine replay --trace-out` does. Exit code and stderr come from the environment.
const fakeEngine = `#!${process.execPath}
const fs = require("node:fs");
const argv = process.argv.slice(2);
const out = argv[argv.indexOf("--trace-out") + 1];
fs.writeFileSync(process.env.FAKE_ENGINE_LOG, JSON.stringify({ argv, cwd: process.cwd(), env: process.env }));
if (process.env.FAKE_ENGINE_FAIL) {
  process.stderr.write(process.env.FAKE_ENGINE_FAIL);
  process.exit(1);
}
fs.writeFileSync(out, process.env.FAKE_ENGINE_TRACE);
`;

const withWorld = (tmp: string): string => {
  const doc = JSON.parse(readFileSync(commandFixture, "utf8"));
  doc.world = { definition_sha256: "0".repeat(64), faults_fired: [], faults_fired_count: 0, state: {}, world_version: 1 };
  const file = join(tmp, "w.attest.json");
  writeFileSync(file, JSON.stringify(doc));
  return file;
};

const draft = (fixtureFile: string, rerun?: { cwd: string; world?: string }): string =>
  renderVitest(loadFixture(fixtureFile), "/p/tests/attest/fixtures/inc.attest.json", "/p/tests/attest/inc.test.ts", rerun);

describe("renderVitest with a rerun", () => {
  it("re-runs the agent and evaluates the contracts on the new trace", () => {
    const source = draft(commandFixture, { cwd: "/p" });

    expect(source).toContain("const trace = await replayRun(FIXTURE, { cwd: RUN_DIR, world: WORLD });");
    expect(source).toContain("await evaluate(contracts(trace));");
    expect(source).toContain("return attestExpect(trace)");
  });

  it("does not evaluate the recording", () => {
    const source = draft(commandFixture, { cwd: "/p" });

    expect(source).not.toContain("FIXTURE.trace");
    expect(source).not.toContain("loadFixture");
    expect(source).not.toContain("inspect");
    expect(source).not.toContain("EXPECTED");
  });

  it("says consistent with the recording, never verified", () => {
    const source = draft(commandFixture, { cwd: "/p" });

    expect(source).toContain("consistent with the recording");
    expect(source).toContain("strict replay with egress blocked");
    expect(source.toLowerCase()).not.toContain("verified");
  });

  it("states the identity limit", () => {
    const source = draft(commandFixture, { cwd: "/p" });

    expect(source).toContain("relative identity");
    expect(source).toContain("absolute");
  });

  it("writes the run directory and world relative to the test", () => {
    const source = draft(commandFixture, { cwd: "/p", world: "/p/world/w.yaml" });

    expect(source).toContain('const RUN_DIR = new URL("../..", import.meta.url);');
    expect(source).toContain('const WORLD = new URL("../../world/w.yaml", import.meta.url);');
  });

  it("leaves the world undefined when the capture hosted none", () => {
    expect(draft(commandFixture, { cwd: "/p" })).toContain("const WORLD = undefined;");
  });

  it("keeps the trace-only draft for a fixture with no command", () => {
    const source = draft(otlpFixture, { cwd: "/p" });

    expect(source).not.toContain("replayRun");
    expect(source).toContain("inspect(contracts())");
  });

  it("keeps the trace-only draft without a rerun", () => {
    const source = draft(commandFixture);

    expect(source).not.toContain("replayRun");
    expect(source).toContain("loadFixture(");
  });
});

describe("replayRun", () => {
  let tmp: string;
  let log: string;
  const saved = { ...process.env };

  beforeEach(() => {
    tmp = mkdtempSync(join(tmpdir(), "attest-replay-draft-"));
    fakeEnginePath.value = join(tmp, "fake-engine");
    writeFileSync(fakeEnginePath.value, fakeEngine);
    chmodSync(fakeEnginePath.value, 0o755);
    log = join(tmp, "engine-call.json");
    process.env.FAKE_ENGINE_LOG = log;
    process.env.FAKE_ENGINE_TRACE = JSON.stringify(JSON.parse(readFileSync(commandFixture, "utf8")).trace);
    delete process.env.FAKE_ENGINE_FAIL;
  });
  afterEach(() => {
    for (const key of Object.keys(process.env)) if (!(key in saved)) delete process.env[key];
    Object.assign(process.env, saved);
  });

  const call = (): { argv: string[]; cwd: string; env: Record<string, string> } =>
    JSON.parse(readFileSync(log, "utf8"));

  it("runs the source command under strict no-live replay", async () => {
    await replayRun(commandFixture, { cwd: tmp });

    const { argv, cwd } = call();
    expect(argv[0]).toBe("replay");
    expect(argv[argv.indexOf("--mode") + 1]).toBe("strict");
    expect(argv).toContain("--no-live");
    expect(argv[argv.indexOf("--fixture") + 1]).toBe(commandFixture);
    expect(argv.slice(argv.indexOf("--") + 1)).toEqual(["agent", "--key", "[redacted]"]);
    expect(cwd).toBe(realpathSync(tmp));
  });

  it("blocks egress and strips provider keys", async () => {
    process.env.ANTHROPIC_API_KEY = "sk-ant-real";
    process.env.OPENAI_API_KEY = "sk-real";

    await replayRun(commandFixture);

    const { env } = call();
    expect(env.ATTEST_EGRESS).toBe("block");
    expect(env.ANTHROPIC_API_KEY).toBeUndefined();
    expect(env.OPENAI_API_KEY).toBe("replay-needs-no-key");
  });

  it("returns the new run's trace", async () => {
    const trace = await replayRun(commandFixture);

    expect(trace).toEqual(loadFixture(commandFixture).trace);
  });

  it("passes the world file", async () => {
    const world = join(tmp, "w.world.yaml");
    writeFileSync(world, "x");

    await replayRun(withWorld(tmp), { world });

    const { argv } = call();
    expect(argv[argv.indexOf("--world") + 1]).toBe(world);
  });

  it("refuses a world fixture without a world file", async () => {
    await expect(replayRun(withWorld(tmp))).rejects.toThrow(/captured with a world/);
  });

  it("rejects with the engine's message when the run fails", async () => {
    process.env.FAKE_ENGINE_FAIL = "replay: 1 request matched no recording";

    await expect(replayRun(commandFixture)).rejects.toThrow(/matched no recording/);
  });

  it("rejects a fixture with no source command", async () => {
    await expect(replayRun(otlpFixture)).rejects.toThrow(CaptureError);
  });
});

describe("captureWorldPath", () => {
  it.each([
    [["--name", "x", "--world", "w.yaml", "--", "cmd"], "w.yaml"],
    [["--name", "x", "--world=w.yaml", "--", "cmd"], "w.yaml"],
    [["--name", "x", "-world", "w.yaml"], "w.yaml"],
    [["--name", "x", "--", "cmd", "--world", "ignored.yaml"], undefined],
    [["--name", "x", "--", "cmd"], undefined],
  ])("reads the world flag of %j", (args, expected) => {
    expect(captureWorldPath(args)).toBe(expected === undefined ? undefined : resolve(expected));
  });
});
