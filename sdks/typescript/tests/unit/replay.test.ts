import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import * as http from "node:http";
import * as https from "node:https";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";

// Fake engines stand in for attest-engine; skip its version check.
vi.mock("../../packages/core/src/engine-manager.js", () => ({
  resolveEngine: async (path?: string) => path ?? "",
}));

import { EgressBlockedError, installEgressGuard, isProviderHost, uninstallEgressGuard } from "../../packages/core/src/egress.js";
import {
  ReplayError,
  ReplayMissError,
  ReplayNotEvaluatedError,
  firstMiss,
  liveLine,
  resolveMode,
  startReplay,
  type ReplaySummary,
} from "../../packages/core/src/replay.js";

function fakeEngine(script: string): string {
  const path = join(mkdtempSync(join(tmpdir(), "attest-fake-engine-")), "attest-engine");
  writeFileSync(path, `#!/bin/sh\n${script}\n`);
  chmodSync(path, 0o755);
  return path;
}

const summary: ReplaySummary = {
  fixture: "inc-1042",
  mode: "strict",
  replayed: 2,
  live: 0,
  misses: 1,
  unused: 0,
  live_cost_usd: 0,
  unpriced_live_calls: 0,
  records: [
    { seq: 0, provider: "openai", api: "chat_completions", outcome: "replayed", exchange: 0 },
    { seq: 1, provider: "openai", api: "chat_completions", outcome: "miss", message: "replay miss: tools changed (hash a→b); re-record" },
  ],
};

describe("resolveMode", () => {
  it("prefers the explicit mode, then ATTEST_REPLAY, then CI, else auto", () => {
    expect(resolveMode("off", { ATTEST_REPLAY: "strict" })).toBe("off");
    expect(resolveMode(undefined, { ATTEST_REPLAY: "auto", CI: "true" })).toBe("auto");
    expect(resolveMode(undefined, { CI: "true" })).toBe("strict");
    expect(resolveMode(undefined, { CI: "false" })).toBe("auto");
    expect(resolveMode(undefined, {})).toBe("auto");
  });

  it("rejects an unknown mode", () => {
    expect(() => resolveMode(undefined, { ATTEST_REPLAY: "loose" })).toThrow(/ATTEST_REPLAY must be strict, auto or off/);
  });
});

describe("summary helpers", () => {
  it("names the first miss and formats the live-call line", () => {
    expect(firstMiss(summary)).toMatch(/^replay miss: tools changed/);
    expect(liveLine(0, 0)).toBe("0 live model calls · $0.00");
    expect(liveLine(1, 0.004)).toBe("1 live model call · $0.00");
    expect(liveLine(3, 1.5, 2)).toBe("3 live model calls · $1.50 (+2 unpriced)");
  });
});

describe("startReplay with a fake engine", () => {
  afterEach(() => {
    delete process.env["OPENAI_BASE_URL"];
  });

  it("sets the environment, stops on finish, restores it and is idempotent", async () => {
    const engine = fakeEngine(
      `echo '{"env":{"OPENAI_BASE_URL":"http://127.0.0.1:1/t/openai/v1"}}'; read line; echo '{"summary":${JSON.stringify(summary)}}'`,
    );
    process.env["OPENAI_BASE_URL"] = "https://before";

    const session = await startReplay("inc-1042", { enginePath: engine, mode: "strict" });
    expect(process.env["OPENAI_BASE_URL"]).toBe("http://127.0.0.1:1/t/openai/v1");
    const [a, b] = await Promise.all([session.finish(), session.finish()]);

    expect(a).toEqual(summary);
    expect(b).toBe(a);
    expect(process.env["OPENAI_BASE_URL"]).toBe("https://before");
    expect(session.summary).toEqual(summary);
  });

  it("reports the engine's stderr when it does not start, and allows a new session", async () => {
    const bad = fakeEngine("echo 'no such fixture' >&2; exit 1");
    await expect(startReplay("x", { enginePath: bad })).rejects.toThrow(/did not start: no such fixture/);

    const ok = fakeEngine(`echo '{"env":{}}'; read line; echo '{"summary":${JSON.stringify(summary)}}'`);
    const session = await startReplay("x", { enginePath: ok });
    await session.finish();
  });

  it("refuses overlapping sessions and invalid names", async () => {
    const ok = fakeEngine(`echo '{"env":{}}'; read line; echo '{"summary":${JSON.stringify(summary)}}'`);
    const first = await startReplay("a", { enginePath: ok });
    await expect(startReplay("b", { enginePath: ok })).rejects.toThrow(/already running/);
    await first.finish();
    await expect(startReplay("../x", { enginePath: ok })).rejects.toBeInstanceOf(ReplayError);
  });

  it("fails when the engine prints no summary", async () => {
    const engine = fakeEngine(`echo '{"env":{}}'; read line; echo oops >&2; exit 3`);
    const session = await startReplay("x", { enginePath: engine });
    await expect(session.finish()).rejects.toThrow(/wrote no summary \(exit 3\): oops/);
  });
});

describe("egress guard", () => {
  afterEach(() => {
    uninstallEgressGuard();
  });

  it("recognises provider hosts and nothing else", () => {
    for (const h of ["api.openai.com", "API.Anthropic.com", "generativelanguage.googleapis.com", "acme.openai.azure.com"]) {
      expect(isProviderHost(h), h).toBe(true);
    }
    for (const h of ["127.0.0.1", "localhost", "example.com", "openai.com", "evilapi.openai.com.example.org"]) {
      expect(isProviderHost(h), h).toBe(false);
    }
  });

  it("fails fetch to a provider host and leaves loopback alone", async () => {
    const server = http.createServer((_req, res) => res.end("ok"));
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    const port = (server.address() as { port: number }).port;
    installEgressGuard();
    try {
      const err = await fetch("https://api.openai.com/v1/models").catch((e: unknown) => e);
      expect(String((err as Error).cause ?? err)).toMatch(/egress guard: connection to model provider api\.openai\.com blocked/);
      expect(((err as Error).cause as Error | undefined)?.name ?? (err as Error).name).toBe("EgressBlockedError");
      const ok = await fetch(`http://127.0.0.1:${String(port)}/`);
      expect(await ok.text()).toBe("ok");
    } finally {
      server.close();
    }
  });

  it("fails https.request and http.request to provider hosts", async () => {
    installEgressGuard();
    for (const request of [https.request, http.request]) {
      const err = await new Promise<Error>((resolve) => {
        const req = request({ host: "api.anthropic.com", path: "/v1/messages", method: "POST" });
        req.on("error", resolve);
        req.on("response", () => resolve(new Error("connected")));
        req.end();
      });
      expect(err).toBeInstanceOf(EgressBlockedError);
    }
  });

  it("lets provider hosts through again after uninstall", async () => {
    const uninstall = installEgressGuard();
    uninstall();
    const err = await new Promise<Error>((resolve) => {
      // Port 1 on an unroutable name would hang; use a provider name that fails to resolve
      // offline or connects: either way the error must not be the guard's.
      const req = http.request({ host: "api.openai.com", port: 1, path: "/", timeout: 500 });
      req.on("error", resolve);
      req.on("timeout", () => req.destroy(new Error("timeout")));
      req.end();
    });
    expect(err).not.toBeInstanceOf(EgressBlockedError);
  });

  it("keeps the guard on until every install is released", async () => {
    const outer = installEgressGuard();
    const inner = installEgressGuard();
    inner();
    inner();
    const err = await new Promise<Error>((resolve) => {
      const req = http.request({ host: "api.openai.com", port: 1, path: "/" });
      req.on("error", resolve);
      req.end();
    });
    outer();
    expect(err).toBeInstanceOf(EgressBlockedError);
  });
});

describe("mode resolution", () => {
  it("treats CI=false and CI=0 as not CI and ignores case and spaces", () => {
    expect(resolveMode(undefined, { CI: "false" })).toBe("auto");
    expect(resolveMode(undefined, { CI: "0" })).toBe("auto");
    expect(resolveMode(undefined, { CI: "true" })).toBe("strict");
    expect(resolveMode(undefined, { ATTEST_REPLAY: " STRICT " })).toBe("strict");
  });

  it("startReplay with mode off returns a no-op session and starts nothing", async () => {
    const session = await startReplay("x", { mode: "off", enginePath: "/nonexistent/engine" });
    expect(session.mode).toBe("off");
    expect(session.env).toEqual({});
    expect(await session.finish()).toBeNull();
    expect(session.error()).toBeNull();
    await session.check();
  });
});


describe("replay errors and engine flags", () => {
  const line = (sum: ReplaySummary): string =>
    fakeEngine(`echo '{"env":{}}'; read line; echo '${JSON.stringify({ summary: sum })}'`);

  it("error() is a miss error in strict mode and not-evaluated in auto mode", async () => {
    const strict = await startReplay("x", { enginePath: line(summary), mode: "strict", noLive: false });
    await strict.finish();
    expect(strict.error()).toBeInstanceOf(ReplayMissError);
    await expect(strict.check()).rejects.toThrow(/replay miss: tools changed/);

    const auto = await startReplay("x", { enginePath: line({ ...summary, mode: "auto" }), mode: "auto", noLive: false });
    await auto.finish();
    expect(auto.error()).toBeInstanceOf(ReplayNotEvaluatedError);
  });

  it("escapes control and format characters in miss and engine-error text", async () => {
    const hostile = { ...summary, records: [{ ...summary.records[1]!, message: "replay miss: model changed (\"a\u001b[31m\\n::error::x\u202e\")" }] };
    const session = await startReplay("x", { enginePath: line(hostile), mode: "strict", noLive: false });
    await session.finish();
    const message = session.error()?.message ?? "";
    // biome-ignore lint/suspicious/noControlCharactersInRegex: asserts that no control byte survives
    expect(message).not.toMatch(/[\u0000-\u001f\u202e]/);
    expect(message).toContain("\\u001b");

    const bad = fakeEngine("printf 'boom\\033[31m\\n::error::x' >&2; exit 1");
    await expect(startReplay("y", { enginePath: bad, noLive: false })).rejects.toThrow(/did not start: boom\\u001b\[31m\\u000a::error::x/);
  });

  it("passes --no-live when blocking and fails a blocked run that went live", async () => {
    const argsFile = join(mkdtempSync(join(tmpdir(), "attest-args-")), "args");
    const engine = fakeEngine(
      `echo "$@" > ${argsFile}; echo '{"env":{}}'; read line; echo '${JSON.stringify({ summary: { ...summary, misses: 0, live: 1, records: [] } })}'`,
    );
    const session = await startReplay("x", { enginePath: engine, mode: "strict", noLive: true });
    await expect(session.finish()).rejects.toThrow(/live model calls while the egress guard is blocking/);
    expect(readFileSync(argsFile, "utf8")).toContain("--no-live");

    const open = await startReplay("x", { enginePath: engine, mode: "strict", noLive: false });
    await open.finish();
    expect(readFileSync(argsFile, "utf8")).not.toContain("--no-live");
  });
});
