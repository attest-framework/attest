// Registered before the test file imports the integration and installs its scope hooks.
import { beforeEach } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";

beforeEach((context) => {
  if (context.task.name === "chain in early beforeEach") {
    attestExpect(new TraceBuilder("early-fixture").setOutput({ message: "actual" }).build())
      .outputContains("absent");
  }
});
