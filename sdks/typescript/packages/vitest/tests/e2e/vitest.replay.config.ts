import { defineConfig } from "vitest/config";
import { AttestReporter } from "../../src/reporter.js";

export default defineConfig({
  test: {
    include: [process.env["REPLAY_TEST_FILE"] ?? "tests/e2e/replay.fixture.ts"],
    reporters: ["default", new AttestReporter()],
  },
});
