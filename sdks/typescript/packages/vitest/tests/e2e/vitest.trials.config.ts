import { defineConfig } from "vitest/config";
import { AttestReporter } from "../../src/reporter.js";

export default defineConfig({
  test: {
    include: ["tests/e2e/trials.fixture.ts"],
    globalSetup: ["tests/e2e/trials.global-setup.ts"],
    reporters: ["default", new AttestReporter()],
  },
});
