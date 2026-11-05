// Standalone script run by integration/transport-routing.test.ts against the built @attest-ai/core.
import { AttestClient, attestExpect, EngineManager, TraceBuilder } from "../../packages/core/dist/esm/index.js";

const engine = new EngineManager({ enginePath: process.env["ATTEST_ENGINE_PATH"] });
await engine.start();
const client = new AttestClient(engine);
const trace = new TraceBuilder("script-agent").setOutput({ message: "ok" }).build();
const result = await client.evaluateBatch(trace, attestExpect(trace).outputContains("ok").assertions);
await engine.stop();
console.log(`WORK_DONE ${result.results.length}`);
