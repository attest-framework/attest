import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import "../../src/index.js";

const trace = new TraceBuilder("cached-a").setOutput({ message: "actual" }).build();
it("cached serial a", () => { attestExpect(trace).outputContains("absent"); });
it.concurrent("cached concurrent a", () => { attestExpect(trace).outputContains("absent"); });
