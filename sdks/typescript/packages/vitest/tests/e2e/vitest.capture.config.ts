import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Runs the Vitest files `attest capture` drafts (written by capture-emit.test.ts into
// tests/e2e/.capture-*), with @attest-ai/vitest resolved to this package's source.
export default defineConfig({
  resolve: {
    alias: { "@attest-ai/vitest": fileURLToPath(new URL("../../src/index.ts", import.meta.url)) },
  },
  test: {
    include: ["tests/e2e/.capture-*/**/*.test.ts"],
  },
});
