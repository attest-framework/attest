// The run's minimum pass rate for trials.fixture.ts, set in the main process.
import { attestGlobalSetup } from "../../src/plugin.js";

const { setup, teardown } = attestGlobalSetup({ minPassRate: 0.5 });
export { setup, teardown };
