// Run by attest-reporter.test.ts with --pool=threads and --pool=forks.
import { registerCases } from "./cases.js";

registerCases("c");
import { it } from "vitest";

it.skip("skipped", () => {});
