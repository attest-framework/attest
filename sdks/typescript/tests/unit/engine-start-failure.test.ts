import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import { beforeEach, describe, expect, it, vi } from "vitest";

const spawnMock = vi.hoisted(() => vi.fn());

vi.mock("node:child_process", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:child_process")>();
  return {
    ...actual,
    spawn: spawnMock,
    spawnSync: vi.fn(() => ({
      error: undefined,
      status: 0,
      stdout: `attest-engine ${process.env["FAKE_ENGINE_VERSION"]}\n`,
      stderr: "",
    })),
  };
});

import { EngineManager } from "../../packages/core/src/engine-manager.js";
import { ENGINE_VERSION } from "../../packages/core/src/version.js";

interface FakeChild extends EventEmitter {
  stdin: EventEmitter & { write: ReturnType<typeof vi.fn> };
  stdout: PassThrough;
  stderr: PassThrough;
  exitCode: number | null;
  signalCode: string | null;
  kill: ReturnType<typeof vi.fn>;
}

/**
 * An engine that died before the initialize write: the write callback reports EPIPE at once,
 * while the child's 'close' (which follows the stderr drain) comes later.
 */
function deadEngine(options: { closeAfterMs: number | null }): FakeChild {
  const child = new EventEmitter() as FakeChild;
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.exitCode = null;
  child.signalCode = null;
  child.stdin = Object.assign(new EventEmitter(), {
    write: vi.fn((_data: string, cb: (err: Error | null) => void) => {
      queueMicrotask(() => cb(Object.assign(new Error("write EPIPE"), { code: "EPIPE" })));
      return false;
    }),
  });
  child.kill = vi.fn((signal: string) => {
    child.signalCode = signal;
    child.emit("exit", null, signal);
    return true;
  });
  setTimeout(() => child.stderr.write("fake-engine: bad configuration\n"), 20);
  if (options.closeAfterMs !== null) {
    setTimeout(() => {
      child.exitCode = 3;
      child.emit("exit", 3, null);
      child.emit("close", 3, null);
    }, options.closeAfterMs);
  }
  return child;
}

describe("EngineManager.start when the engine dies before initialize", () => {
  beforeEach(() => {
    spawnMock.mockReset();
    process.env["FAKE_ENGINE_VERSION"] = ENGINE_VERSION;
  });

  it("rejects with the engine's stderr line when EPIPE arrives before close", async () => {
    spawnMock.mockReturnValue(deadEngine({ closeAfterMs: 60 }));
    const manager = new EngineManager({ enginePath: process.execPath });

    await expect(manager.start()).rejects.toThrow(/fake-engine: bad configuration/);
  });

  it("does not hang when a grandchild keeps stderr open so close never fires", async () => {
    spawnMock.mockReturnValue(deadEngine({ closeAfterMs: null }));
    const manager = new EngineManager({ enginePath: process.execPath });
    const startedAt = Date.now();

    await expect(manager.start()).rejects.toThrow(/EPIPE/);
    expect(Date.now() - startedAt).toBeLessThan(5_000);
  }, 10_000);

  it("keeps a killed engine's late stderr out of the restarted engine's error", async () => {
    const old = new EventEmitter() as FakeChild;
    old.stdout = new PassThrough();
    old.stderr = new PassThrough();
    old.exitCode = null;
    old.signalCode = null;
    old.stdin = Object.assign(new EventEmitter(), {
      write: vi.fn((data: string, cb: (err: Error | null) => void) => {
        const { id } = JSON.parse(data) as { id: number };
        old.stdout.write(
          JSON.stringify({
            jsonrpc: "2.0",
            id,
            result: { engine_version: ENGINE_VERSION, compatible: true, capabilities: [] },
          }) + "\n",
        );
        cb(null);
        return true;
      }),
    });
    old.kill = vi.fn((signal: string) => {
      old.signalCode = signal;
      old.emit("exit", null, signal);
      setTimeout(() => old.stderr.write("OLD-ENGINE-LATE\n"), 30);
      return true;
    });
    spawnMock.mockReturnValueOnce(old).mockReturnValueOnce(deadEngine({ closeAfterMs: 80 }));
    const manager = new EngineManager({ enginePath: process.execPath });
    await manager.start();

    const restart = manager.restartAfterTimeout([]);

    await expect(restart).rejects.toThrow(/fake-engine: bad configuration/);
    await expect(restart).rejects.not.toThrow(/OLD-ENGINE-LATE/);
  });

  it("wraps a write error after startup with the engine's stderr tail", async () => {
    const child = new EventEmitter() as FakeChild;
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.exitCode = null;
    child.signalCode = null;
    let writes = 0;
    child.stdin = Object.assign(new EventEmitter(), {
      write: vi.fn((data: string, cb: (err: Error | null) => void) => {
        writes += 1;
        if (writes > 1) {
          queueMicrotask(() => cb(Object.assign(new Error("write EPIPE"), { code: "EPIPE" })));
          return false;
        }
        const { id } = JSON.parse(data) as { id: number };
        child.stdout.write(
          JSON.stringify({
            jsonrpc: "2.0",
            id,
            result: { engine_version: ENGINE_VERSION, compatible: true, capabilities: [] },
          }) + "\n",
        );
        cb(null);
        return true;
      }),
    });
    child.kill = vi.fn((signal: string) => {
      child.signalCode = signal;
      child.emit("exit", null, signal);
      return true;
    });
    spawnMock.mockReturnValue(child);
    const manager = new EngineManager({ enginePath: process.execPath });
    await manager.start();
    child.stderr.write("fake-engine: out of memory\n");
    await new Promise((done) => setTimeout(done, 20));

    await expect(manager.sendRequest("shutdown", {})).rejects.toThrow(/out of memory/);
    await manager.stop();
  });
});
