import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type * as TS from "typescript";

const here = dirname(fileURLToPath(import.meta.url));
export const TS_ROOT = resolve(here, "../..");
export const PARITY_DIR = resolve(TS_ROOT, "../parity");
export const UPDATE_ENV = "ATTEST_UPDATE_PARITY";

const CORE = resolve(TS_ROOT, "packages/core");
const ts = createRequire(resolve(CORE, "package.json"))("typescript") as typeof TS;

export const SECTIONS = [
  "adapters",
  "exports",
  "expect_chain",
  "init_flags",
  "plugins",
  "simulation",
  "test_runner",
] as const;
export type Section = (typeof SECTIONS)[number];
export type Surface = Record<Section, string[]>;

/** Drift messages between a committed snapshot and the live surface. */
export function compareSurface(committed: Surface, live: Surface): string[] {
  const messages: string[] = [];
  for (const section of SECTIONS) {
    const have = new Set(committed[section] ?? []);
    const want = new Set(live[section] ?? []);
    for (const name of want) if (!have.has(name)) messages.push(`added ${section}.${name}`);
    for (const name of have) if (!want.has(name)) messages.push(`removed ${section}.${name}`);
  }
  return messages;
}

export function serializeSurface(surface: Surface): string {
  const sorted = Object.fromEntries(
    SECTIONS.map((s) => [s, [...new Set(surface[s])].sort()]),
  );
  return `${JSON.stringify(sorted, null, 2)}\n`;
}

function program(file: string): { checker: TS.TypeChecker; module: TS.Symbol } {
  const prog = ts.createProgram([file], {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    skipLibCheck: true,
    noEmit: true,
    types: [],
  });
  const checker = prog.getTypeChecker();
  const source = prog.getSourceFile(file);
  const module = source === undefined ? undefined : checker.getSymbolAtLocation(source);
  if (module === undefined) throw new Error(`cannot resolve module symbol for ${file}`);
  return { checker, module };
}

function publicName(name: string): boolean {
  return !name.startsWith("_") && name !== "default";
}

/** Public export names (values and types) of a module, following `export *`. */
export function moduleExports(file: string): string[] {
  const { checker, module } = program(file);
  return checker
    .getExportsOfModule(module)
    .map((s) => s.name)
    .filter(publicName)
    .sort();
}

/** Exported classes of a module (excludes interfaces and type aliases). */
function exportedClasses(file: string): string[] {
  const { checker, module } = program(file);
  return checker
    .getExportsOfModule(module)
    .filter((s) => {
      const target = s.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(s) : s;
      return (target.flags & ts.SymbolFlags.Class) !== 0;
    })
    .map((s) => s.name)
    .sort();
}

/** Public (non-private, non-protected) instance members of an exported class. */
export function classMembers(file: string, className: string): string[] {
  const { checker, module } = program(file);
  const exported = checker.getExportsOfModule(module).find((s) => s.name === className);
  if (exported === undefined) throw new Error(`${className} is not exported from ${file}`);
  const target = exported.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(exported) : exported;
  const type = checker.getDeclaredTypeOfSymbol(target);
  const hidden = ts.ModifierFlags.Private | ts.ModifierFlags.Protected;
  return checker
    .getPropertiesOfType(type)
    .filter((p) => publicName(p.name) && !p.name.startsWith("#"))
    .filter((p) => (p.declarations ?? []).every((d) => (ts.getCombinedModifierFlags(d as TS.Declaration) & hidden) === 0))
    .map((p) => p.name)
    .sort();
}

/** Flags accepted by `attest init`, read from the argument parser in cli.ts. */
export function initFlags(): string[] {
  const src = readFileSync(resolve(CORE, "src/cli.ts"), "utf8");
  const start = src.indexOf("function parseInitArgs");
  const end = src.indexOf("\nfunction ", start + 1);
  if (start < 0 || end < 0) throw new Error("parseInitArgs not found in cli.ts");
  const flags = [...src.slice(start, end).matchAll(/"(--[a-z][a-z-]*)(?:=[a-z]+)?"/g)].map((m) => m[1] as string);
  return [...new Set(flags)].sort();
}

export function collectSurface(): Surface {
  const core = resolve(CORE, "src");
  const exported = moduleExports(resolve(core, "index.ts"));
  // Only names re-exported from the package entry count: users cannot import the rest.
  const reachable = (file: string): string[] => moduleExports(file).filter((n) => exported.includes(n));
  return {
    adapters: exportedClasses(resolve(core, "adapters/index.ts")),
    exports: exported,
    expect_chain: classMembers(resolve(core, "index.ts"), "ExpectChain"),
    init_flags: initFlags(),
    plugins: reachable(resolve(core, "plugins.ts")),
    simulation: reachable(resolve(core, "simulation/index.ts")),
    test_runner: moduleExports(resolve(TS_ROOT, "packages/vitest/src/index.ts")),
  };
}
