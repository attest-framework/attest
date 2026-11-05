import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

const CORE = fileURLToPath(new URL("../../../packages/core/", import.meta.url));
const TSC = fileURLToPath(new URL("../../../packages/core/node_modules/typescript/bin/tsc", import.meta.url));

describe("LangChainAdapter typings", () => {
  it("accepts the adapter in langchainjs callbacks under tsc --strict against the built .d.ts", () => {
    expect(existsSync(`${CORE}dist/esm/index.d.ts`), "run `pnpm build` before `pnpm test`").toBe(true);

    let output = "";
    try {
      execFileSync(process.execPath, [TSC, "-p", `${CORE}type-tests/tsconfig.json`], { encoding: "utf8" });
    } catch (error) {
      output = (error as { stdout?: string }).stdout ?? String(error);
    }

    expect(output).toBe("");
  }, 60_000);
});

