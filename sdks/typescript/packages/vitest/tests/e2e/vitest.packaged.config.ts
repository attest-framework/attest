import { defineConfig } from "vitest/config";

// The reporter and global setup load by their published subpaths (built dist), in the main process.
export default defineConfig({
  test: {
    include: ["tests/e2e/packaged.fixture.ts"],
    globalSetup: ["@attest-ai/vitest/setup"],
    reporters: ["default", "@attest-ai/vitest/reporter"],
  },
});
