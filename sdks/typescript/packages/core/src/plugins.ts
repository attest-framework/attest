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

  const timeoutPromise = new Promise<never>((_, reject) => {
    setTimeout(() => reject(new Error(`Plugin '${plugin.name}' timed out after ${timeoutMs}ms`)), timeoutMs);
  });

  const result = await Promise.race([resultPromise, timeoutPromise]);

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
