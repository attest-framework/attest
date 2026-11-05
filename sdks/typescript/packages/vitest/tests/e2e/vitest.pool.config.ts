import { defineConfig } from "vitest/config";
import { AttestReporter } from "../../src/reporter.js";

export default defineConfig({
  test: {
    include: ["tests/e2e/pool/*.fixture.ts"],
    reporters: ["default", new AttestReporter()],
  },
});
