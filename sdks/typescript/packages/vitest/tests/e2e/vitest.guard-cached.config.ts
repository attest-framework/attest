import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["tests/e2e/guard-cached-*.fixture.ts"],
    isolate: false,
    fileParallelism: false,
    maxWorkers: 1,
    reporters: ["json"],
  },
});
