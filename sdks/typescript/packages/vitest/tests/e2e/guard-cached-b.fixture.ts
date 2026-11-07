import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import "../../src/index.js";

const trace = new TraceBuilder("cached-b").setOutput({ message: "actual" }).build();
it("cached serial b", () => { attestExpect(trace).outputContains("absent"); });
it.concurrent("cached concurrent b", () => { attestExpect(trace).outputContains("absent"); });
