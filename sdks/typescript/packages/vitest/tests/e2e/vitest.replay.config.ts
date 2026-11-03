import { defineConfig } from "vitest/config";
import { AttestReporter } from "../../src/reporter.js";

export default defineConfig({
  test: {
    include: ["tests/e2e/replay.fixture.ts"],
    reporters: ["default", new AttestReporter()],
  },
});
