import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PluginRegistry,
  executePluginAssertion,
  loadPackagePlugins,
} from "../../packages/core/src/index.js";
import type { AttestClient, AttestPlugin, Trace } from "../../packages/core/src/index.js";

let root: string;

function writeJson(path: string, value: unknown): void {
  mkdirSync(join(path, ".."), { recursive: true });
  writeFileSync(path, JSON.stringify(value));
}

function installPackage(name: string, plugins: string[] | undefined, files: Record<string, string>): void {
  const dir = join(root, "node_modules", name);
  writeJson(join(dir, "package.json"), {
    name,
    version: "1.0.0",
    ...(plugins ? { attest: { plugins } } : {}),
  });
  for (const [file, body] of Object.entries(files)) {
    mkdirSync(join(dir, file, ".."), { recursive: true });
    writeFileSync(join(dir, file), body);
  }
}

const PLUGIN_SRC = (name: string): string =>
  `export default { name: "${name}", pluginType: "assertion", execute: () => ({ status: "pass", score: 1, explanation: "ok" }) };`;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "attest-plugins-"));
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
  vi.restoreAllMocks();
});

describe("loadPackagePlugins", () => {
  it("registers the default export of a declared plugin entry", async () => {
    writeJson(join(root, "package.json"), { dependencies: { "attest-plugin-safety": "1.0.0" } });
    installPackage("attest-plugin-safety", ["./plugin.mjs"], { "plugin.mjs": PLUGIN_SRC("safety") });

    const registry = new PluginRegistry();
    expect(await loadPackagePlugins(registry, root)).toBe(1);
    expect(registry.listPlugins()).toEqual(["assertion/safety"]);
  });

  it("registers every plugin-shaped named export when there is no default", async () => {
    writeJson(join(root, "package.json"), { devDependencies: { "multi-plugin": "1.0.0" } });
    const one = `{ name: "one", pluginType: "judge", execute: () => ({ status: "pass", score: 1, explanation: "" }) }`;
    const two = `{ name: "two", pluginType: "judge", execute: () => ({ status: "pass", score: 1, explanation: "" }) }`;
    installPackage("multi-plugin", ["./p.mjs"], {
      "p.mjs": `export const one = ${one}; export const two = ${two}; export const helper = 3;`,
    });

    const registry = new PluginRegistry();
    expect(await loadPackagePlugins(registry, root)).toBe(2);
    expect(registry.listPlugins("judge").sort()).toEqual(["one", "two"]);
  });

  it("ignores dependencies without an attest.plugins declaration or install", async () => {
    writeJson(join(root, "package.json"), {
      dependencies: { plain: "1.0.0", missing: "1.0.0" },
    });
    installPackage("plain", undefined, {});

    expect(await loadPackagePlugins(new PluginRegistry(), root)).toBe(0);
  });

  it("logs a broken entry and still loads the others", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    writeJson(join(root, "package.json"), { dependencies: { "mixed-plugin": "1.0.0" } });
    installPackage("mixed-plugin", ["./broken.mjs", "./empty.mjs", "./good.mjs", "../../escape.mjs"], {
      "broken.mjs": "throw new Error('boom');",
      "empty.mjs": "export const x = 1;",
      "good.mjs": PLUGIN_SRC("good"),
    });

    const registry = new PluginRegistry();
    expect(await loadPackagePlugins(registry, root)).toBe(1);
    expect(registry.listPlugins()).toEqual(["assertion/good"]);
    expect(errors).toHaveBeenCalledTimes(3);
  });

  it("loads CommonJS plugins: module.exports, exports.default and named exports", async () => {
    writeJson(join(root, "package.json"), { dependencies: { "cjs-plugin": "1.0.0" } });
    const body = (name: string): string =>
      `{ name: "${name}", pluginType: "assertion", execute: () => ({ status: "pass", score: 1, explanation: "" }) }`;
    installPackage("cjs-plugin", ["./a.cjs", "./b.cjs", "./c.cjs"], {
      "a.cjs": `module.exports = ${body("a")};`,
      "b.cjs": `Object.defineProperty(exports, "__esModule", { value: true }); exports.default = ${body("b")};`,
      "c.cjs": `exports.plugin = ${body("c")};`,
    });

    const registry = new PluginRegistry();
    expect(await loadPackagePlugins(registry, root)).toBe(3);
    expect(registry.listPlugins("assertion").sort()).toEqual(["a", "b", "c"]);
  });

  it("logs a malformed attest.plugins declaration and a bad package.json, then continues", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    writeJson(join(root, "package.json"), {
      dependencies: { "bad-shape": "1", "bad-json": "1", "good-plugin": "1" },
    });
    writeJson(join(root, "node_modules", "bad-shape", "package.json"), { attest: { plugins: "./p.mjs" } });
    mkdirSync(join(root, "node_modules", "bad-json"), { recursive: true });
    writeFileSync(join(root, "node_modules", "bad-json", "package.json"), "{ not json");
    installPackage("good-plugin", ["./plugin.mjs"], { "plugin.mjs": PLUGIN_SRC("good") });

    const registry = new PluginRegistry();
    expect(await loadPackagePlugins(registry, root)).toBe(1);
    expect(errors).toHaveBeenCalledTimes(2);
  });

  it("throws when the project has no package.json", async () => {
    await expect(loadPackagePlugins(new PluginRegistry(), root)).rejects.toThrow();
  });
});

describe("executePluginAssertion", () => {
  const trace = { trace_id: "t", output: {} } as unknown as Trace;
  const client = {
    submitPluginResult: vi.fn().mockResolvedValue(undefined),
  } as unknown as AttestClient;

  it("submits the result and clears the timeout timer", async () => {
    vi.useFakeTimers();
    try {
      const plugin: AttestPlugin = {
        name: "p",
        pluginType: "assertion",
        execute: () => ({ status: "pass", score: 1, explanation: "ok" }),
      };
      const result = await executePluginAssertion(plugin, trace, {}, client, "t", "a", 30_000);
      expect(result.status).toBe("pass");
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("rejects with a timeout error for a slow plugin", async () => {
    const plugin: AttestPlugin = {
      name: "slow",
      pluginType: "assertion",
      execute: () => new Promise(() => undefined),
    };
    await expect(executePluginAssertion(plugin, trace, {}, client, "t", "a", 10)).rejects.toThrow(
      "Plugin 'slow' timed out after 10ms",
    );
  });
});
