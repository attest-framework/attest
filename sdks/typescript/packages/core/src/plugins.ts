import { existsSync, readFileSync, realpathSync } from "node:fs";
import { dirname, join, relative, resolve, isAbsolute, sep } from "node:path";
import { pathToFileURL } from "node:url";
import type { Trace } from "./proto/types.js";
import type { AttestClient } from "./client.js";

export interface PluginResult {
  readonly status: string;
  readonly score: number;
  readonly explanation: string;
  readonly metadata?: Record<string, unknown>;
}

export interface AttestPlugin {
  readonly name: string;
  readonly pluginType: string; // "adapter" | "assertion" | "judge" | "reporter"
  execute(trace: Trace, spec: Record<string, unknown>): PluginResult | Promise<PluginResult>;
}

export class PluginRegistry {
  private readonly plugins = new Map<string, Map<string, AttestPlugin>>();

  register(name: string, plugin: AttestPlugin): void {
    const pluginType = plugin.pluginType;
    let typeMap = this.plugins.get(pluginType);
    if (typeMap === undefined) {
      typeMap = new Map();
      this.plugins.set(pluginType, typeMap);
    }
    typeMap.set(name, plugin);
  }

  get(pluginType: string, name: string): AttestPlugin | undefined {
    return this.plugins.get(pluginType)?.get(name);
  }

  listPlugins(pluginType?: string): string[] {
    if (pluginType !== undefined) {
      const typeMap = this.plugins.get(pluginType);
      return typeMap !== undefined ? [...typeMap.keys()] : [];
    }
    const result: string[] = [];
    for (const [ptype, typeMap] of this.plugins) {
      for (const pname of typeMap.keys()) {
        result.push(`${ptype}/${pname}`);
      }
    }
    return result;
  }
}

export async function executePluginAssertion(
  plugin: AttestPlugin,
  trace: Trace,
  spec: Record<string, unknown>,
  client: AttestClient,
  traceId: string,
  assertionId: string,
  timeoutMs = 30_000,
): Promise<PluginResult> {
  const resultPromise = Promise.resolve(plugin.execute(trace, spec));

  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeoutPromise = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error(`Plugin '${plugin.name}' timed out after ${timeoutMs}ms`)),
      timeoutMs,
    );
  });

  let result: PluginResult;
  try {
    result = await Promise.race([resultPromise, timeoutPromise]);
  } finally {
    clearTimeout(timer);
  }

  await client.submitPluginResult(
    traceId,
    plugin.name,
    assertionId,
    result.status,
    result.score,
    result.explanation,
  );

  return result;
}

/** Explicitly register a plugin instance into the registry. */
export function registerPlugin(registry: PluginRegistry, name: string, plugin: AttestPlugin): void {
  registry.register(name, plugin);
}

function isAttestPlugin(value: unknown): value is AttestPlugin {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v["name"] === "string" &&
    typeof v["pluginType"] === "string" &&
    typeof v["execute"] === "function"
  );
}

function readJson(path: string): Record<string, unknown> {
  return JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
}

function findInstalledPackage(projectRoot: string, name: string): string | undefined {
  let dir = resolve(projectRoot);
  for (;;) {
    const candidate = join(dir, "node_modules", name);
    if (existsSync(join(candidate, "package.json"))) return candidate;
    const parent = dirname(dir);
    if (parent === dir) return undefined;
    dir = parent;
  }
}

function declaredPluginPaths(pkgJson: Record<string, unknown>, dep: string): string[] {
  const attest = pkgJson["attest"];
  if (attest === undefined) return [];
  const plugins =
    typeof attest === "object" && attest !== null
      ? (attest as Record<string, unknown>)["plugins"]
      : undefined;
  if (plugins === undefined) return [];
  if (!Array.isArray(plugins) || !plugins.every((p) => typeof p === "string")) {
    throw new Error(`'${dep}': "attest.plugins" must be an array of strings`);
  }
  return plugins as string[];
}

/**
 * Plugins exported by a loaded module: the default export if it is a plugin
 * (a CommonJS build puts `module.exports` there, so also look one `default`
 * deeper), otherwise every plugin-shaped named export.
 */
function pluginsOf(mod: Record<string, unknown>): AttestPlugin[] {
  const def = mod["default"];
  const nested = typeof def === "object" && def !== null ? (def as Record<string, unknown>) : undefined;
  for (const candidate of [def, nested?.["default"]]) {
    if (isAttestPlugin(candidate)) return [candidate];
  }
  return [...new Set([...Object.values(mod), ...Object.values(nested ?? {})])].filter(isAttestPlugin);
}

function isInside(root: string, file: string): boolean {
  const within = (r: string, f: string): boolean => {
    const rel = relative(r, f);
    return rel !== "" && rel !== ".." && !rel.startsWith(`..${sep}`) && !isAbsolute(rel);
  };
  // Lexical check first, then resolve symlinks on both sides (throws when the file is missing).
  return within(root, file) && within(realpathSync(root), realpathSync(file));
}

/**
 * Discover and load plugins declared by the project's direct dependencies.
 *
 * An installed dependency declares plugins in its `package.json` as
 * `"attest": {"plugins": ["./dist/plugin.js"]}`. Each entry is imported and
 * its default export (or, without one, every export implementing the
 * plugin shape) is registered under the plugin's `name`. A failure loading
 * one entry, or reading the project's `package.json`, is logged and does not
 * raise, as with Python's `load_entrypoint_plugins`. Dependencies that are declared but not
 * installed are skipped. Returns the number of plugins loaded.
 */
export async function loadPackagePlugins(
  registry: PluginRegistry,
  projectRoot: string = process.cwd(),
): Promise<number> {
  let project: Record<string, unknown>;
  try {
    project = readJson(join(projectRoot, "package.json"));
  } catch (err) {
    console.error(`attest: cannot read ${join(projectRoot, "package.json")}; no plugins loaded:`, err);
    return 0;
  }
  const depNames = new Set<string>();
  for (const field of ["dependencies", "devDependencies", "optionalDependencies"]) {
    const deps = project[field];
    if (typeof deps === "object" && deps !== null) {
      for (const dep of Object.keys(deps)) depNames.add(dep);
    }
  }

  let loaded = 0;
  for (const dep of depNames) {
    const pkgDir = findInstalledPackage(projectRoot, dep);
    if (pkgDir === undefined) continue;
    let entries: string[];
    try {
      entries = declaredPluginPaths(readJson(join(pkgDir, "package.json")), dep);
    } catch (err) {
      console.error(`attest: failed to read plugins declared by '${dep}':`, err);
      continue;
    }
    for (const entry of entries) {
      try {
        const file = resolve(pkgDir, entry);
        if (!isInside(pkgDir, file)) {
          throw new Error(`plugin path '${entry}' escapes package '${dep}'`);
        }
        const mod = (await import(pathToFileURL(file).href)) as Record<string, unknown>;
        const plugins = pluginsOf(mod);
        if (plugins.length === 0) {
          throw new Error(`'${entry}' exports no plugin (name, pluginType, execute)`);
        }
        for (const plugin of plugins) {
          registry.register(plugin.name, plugin);
          loaded++;
        }
      } catch (err) {
        console.error(`attest: failed to load plugin '${entry}' from '${dep}':`, err);
      }
    }
  }
  return loaded;
}
