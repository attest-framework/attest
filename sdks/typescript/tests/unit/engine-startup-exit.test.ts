import { chmodSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ENGINE_VERSION, EngineManager } from "../../packages/core/src/index.js";

describe("EngineManager.start when the engine exits before ready", () => {
  it("rejects with the stderr tail even when stdout EOF precedes the stderr write", async () => {
    // Arrange: stdout closes first; stderr arrives 300 ms later, then exit 3.
    const dir = mkdtempSync(join(tmpdir(), "attest-late-stderr-"));
    const engine = join(dir, "attest-engine");
    writeFileSync(
      engine,
      [
        "#!/bin/sh",
        `if [ "$1" = version ]; then echo "attest-engine ${ENGINE_VERSION}"; exit 0; fi`,
        "exec 1>&-",
        "sleep 0.3",
        'echo "engine boot failure: bad configuration" >&2',
        "exit 3",
        "",
      ].join("\n"),
    );
    chmodSync(engine, 0o755);
    const manager = new EngineManager({ enginePath: engine, logLevel: "warn" });

    // Act / Assert
    await expect(manager.start()).rejects.toThrow(/engine boot failure: bad configuration/);
  }, 15_000);
});
