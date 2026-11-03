import { existsSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve, isAbsolute } from "node:path";
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

function declaredPluginPaths(pkgJson: Record<string, unknown>): string[] {
  const attest = pkgJson["attest"];
  if (typeof attest !== "object" || attest === null) return [];
  const plugins = (attest as Record<string, unknown>)["plugins"];
  return Array.isArray(plugins) ? plugins.filter((p): p is string => typeof p === "string") : [];
}

/**
 * Discover and load plugins declared by the project's direct dependencies.
 *
 * An installed dependency declares plugins in its `package.json` as
 * `"attest": {"plugins": ["./dist/plugin.js"]}`. Each entry is imported and
 * its default export (or, without one, every export implementing the
 * plugin shape) is registered under the plugin's `name`. A failure loading
 * one entry is logged and does not stop the others, as with Python's
 * `load_entrypoint_plugins`. Dependencies that are declared but not
 * installed are skipped. Returns the number of plugins loaded.
 */
export async function loadPackagePlugins(
  registry: PluginRegistry,
  projectRoot: string = process.cwd(),
): Promise<number> {
  const project = readJson(join(projectRoot, "package.json"));
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
    for (const entry of declaredPluginPaths(readJson(join(pkgDir, "package.json")))) {
      try {
        const file = resolve(pkgDir, entry);
        const rel = relative(pkgDir, file);
        if (rel.startsWith("..") || isAbsolute(rel)) {
          throw new Error(`plugin path '${entry}' escapes package '${dep}'`);
        }
        const mod = (await import(pathToFileURL(file).href)) as Record<string, unknown>;
        const candidates =
          mod["default"] !== undefined
            ? [mod["default"]]
            : Object.values(mod).filter(isAttestPlugin);
        const plugins = candidates.filter(isAttestPlugin);
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
