import { describe, it, expect } from "vitest";
import type { AttestPlugin } from "../../packages/core/src/plugins.js";
import { PluginRegistry } from "../../packages/core/src/plugins.js";

function makePlugin(name: string, pluginType: string): AttestPlugin {
  return {
    name,
    pluginType,
    execute: () => ({ status: "pass", score: 1.0, explanation: "ok" }),
  };
}

describe("PluginRegistry", () => {
  it("registers and retrieves a plugin", () => {
    const registry = new PluginRegistry();
    const plugin = makePlugin("safety", "assertion");
    registry.register("safety", plugin);

    expect(registry.get("assertion", "safety")).toBe(plugin);
  });

  it("returns undefined for unknown plugin", () => {
    const registry = new PluginRegistry();
    expect(registry.get("assertion", "unknown")).toBeUndefined();
  });

  it("lists plugins by type", () => {
    const registry = new PluginRegistry();
    registry.register("a", makePlugin("a", "assertion"));
    registry.register("b", makePlugin("b", "assertion"));
    registry.register("c", makePlugin("c", "reporter"));

    expect(registry.listPlugins("assertion")).toEqual(["a", "b"]);
    expect(registry.listPlugins("reporter")).toEqual(["c"]);
    expect(registry.listPlugins("judge")).toEqual([]);
  });

  it("lists all plugins as type/name strings", () => {
    const registry = new PluginRegistry();
    registry.register("a", makePlugin("a", "assertion"));
    registry.register("b", makePlugin("b", "reporter"));

    const all = registry.listPlugins();
    expect(all).toContain("assertion/a");
    expect(all).toContain("reporter/b");
  });
});
