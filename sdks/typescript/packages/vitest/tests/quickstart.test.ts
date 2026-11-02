/**
 * The TypeScript quickstart as a user runs it: the README's `pnpm exec` commands and the
 * project `attest init` generates, in a fresh directory with HOME redirected and no
 * provider keys. The project's node_modules links the built workspace packages, so
 * run `pnpm build` first. Skipped when the engine has not been built (`make engine`).
 */
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { engine, repoRoot, reportV2Validator } from "./report-schema.js";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const coreDir = resolve(pkgDir, "../core");
const cli = join(coreDir, "dist/esm/cli.js");
const vitestBin = join(pkgDir, "node_modules/.bin/vitest");
const MARKER = "ATTEST-DEMO";
const REPORT = ["--reporter=default", "--reporter=@attest-ai/vitest/reporter", "--outputFile.attest=r.json"];

/** An empty npm project whose node_modules links this workspace's packages. */
function newProject(): string {
  const dir = join(mkdtempSync(join(tmpdir(), "attest-quickstart-")), "project");
  mkdirSync(join(dir, "node_modules", "@attest-ai"), { recursive: true });
  writeFileSync(join(dir, "package.json"), JSON.stringify({ name: "project", type: "module" }));
  symlinkSync(coreDir, join(dir, "node_modules/@attest-ai/core"));
  symlinkSync(pkgDir, join(dir, "node_modules/@attest-ai/vitest"));
  symlinkSync(join(pkgDir, "node_modules/vitest"), join(dir, "node_modules/vitest"));
  return dir;
}

/** Run a README `pnpm exec <argv>` command the way the quickstart does. */
function npx(project: string, argv: string[]) {
  const [tool, ...rest] = argv;
  const command: Record<string, string[]> = {
    attest: [process.execPath, cli],
    vitest: [vitestBin],
  };
  const prefix = command[tool ?? ""];
  if (prefix === undefined) throw new Error(`no runner for npx ${argv.join(" ")}`);
  const proc = spawnSync(prefix[0]!, [...prefix.slice(1), ...rest], {
    cwd: project,
    encoding: "utf8",
    env: {
      PATH: process.env["PATH"] ?? "",
      HOME: join(project, "..", "home"),
      ATTEST_ENGINE_PATH: engine,
      ATTEST_ENGINE_NO_DOWNLOAD: "1",
      NO_COLOR: "1",
    },
  });
  return { status: proc.status, out: `${proc.stdout}${proc.stderr}` };
}

/** Shell lines of the README's TypeScript quickstart, run here; project setup and installs are provided. */
function readmeTypeScriptCommands(readme: string): string[][] {
  const section = readme.slice(readme.indexOf("### TypeScript"), readme.indexOf("### Build from source"));
  const blocks = [...section.matchAll(/^```bash\n([\s\S]*?)^```$/gm)].map((m) => m[1] ?? "");
  expect(blocks.length).toBeGreaterThan(0);
  return blocks
    .flatMap((b) => b.split("\n"))
    .flatMap((line) => line.split("&&"))
    .map((c) => c.trim())
    .filter((c) => c !== "" && !c.startsWith("#") && !/^pnpm (init|pkg|add)\b/.test(c))
    .map((c) => {
      const argv = c.split(/\s+/);
      if (argv[0] !== "pnpm" || argv[1] !== "exec") throw new Error(`no runner for README command ${c}`);
      return argv.slice(2);
    });
}

function editMarkedLine(project: string, from: string, to: string): void {
  const file = join(project, "tests/my-agent.test.ts");
  const lines = readFileSync(file, "utf8").split("\n");
  const marked = lines.findIndex((l) => l.includes(MARKER));
  lines[marked] = lines[marked]!.replace(from, to);
  writeFileSync(file, lines.join("\n"));
}

describe.skipIf(!existsSync(engine) || !existsSync(cli))("TypeScript quickstart (real engine)", () => {
  it("README commands pass, the marked edit fails with a diagnostic, and the gate follows", () => {
    const project = newProject();
    for (const argv of readmeTypeScriptCommands(readFileSync(join(repoRoot, "README.md"), "utf8"))) {
      const run = npx(project, argv);
      expect(run.status, `npx ${argv.join(" ")}\n${run.out}`).toBe(0);
    }
    expect(npx(project, ["vitest", "run"]).out).toMatch(/Tests\s+1 passed/);

    editMarkedLine(project, '"refund"', '"store credit"');
    const red = npx(project, ["vitest", "run", ...REPORT]);
    expect(red.status, red.out).toBe(1);
    expect(red.out).toMatch(/Tests\s+1 failed/);
    expect(red.out).toContain("L4 content");
    expect(red.out).toContain('expected:   contains "store credit"');
    expect(red.out).toContain("actual:     Your refund of $42.00 has been issued.");

    const report = JSON.parse(readFileSync(join(project, "r.json"), "utf8")) as {
      results: Array<{ status: string; file: string; line: number }>;
    };
    const validate = reportV2Validator();
    expect(validate(report), JSON.stringify(validate.errors)).toBe(true);
    const source = readFileSync(join(project, "tests/my-agent.test.ts"), "utf8").split("\n");
    const itLine = source.findIndex((l) => l.startsWith("it(")) + 1;
    expect(report.results.filter((r) => r.status === "hard_fail")).toEqual([
      expect.objectContaining({ file: "tests/my-agent.test.ts", line: itLine }),
    ]);
    const policy = ["attest", "policy", "evaluate", "--report", "r.json", "--policy", "attest.policy.yaml"];
    expect(npx(project, policy).status).toBe(1);

    editMarkedLine(project, '"store credit"', '"refund"');
    expect(npx(project, ["vitest", "run", ...REPORT]).status).toBe(0);
    expect(npx(project, policy).status).toBe(0);
  }, 120_000);

  it("writes the report beside an existing Vitest config", () => {
    const project = newProject();
    writeFileSync(
      join(project, "vitest.config.ts"),
      'import { defineConfig } from "vitest/config";\nexport default defineConfig({ test: { reporters: ["dot"] } });\n',
    );
    expect(npx(project, ["attest", "init"]).status).toBe(0);

    const run = npx(project, ["vitest", "run", ...REPORT]);

    expect(run.status, run.out).toBe(0);
    const report = JSON.parse(readFileSync(join(project, "r.json"), "utf8")) as { tests: unknown[] };
    expect(report.tests).toHaveLength(1);
  }, 60_000);

  it("fails on a README command it cannot run", () => {
    expect(() => readmeTypeScriptCommands("### TypeScript\n```bash\nyarn attest init\n```\n### Build from source")).toThrow(
      "no runner for README command yarn attest init",
    );
  });
});
