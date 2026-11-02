/**
 * record() against the engine built by `make engine`. A scripted agent calls a fake
 * OpenAI-compatible upstream through the capture proxy and exports the INC-1042 shape
 * (one lookup, two refunds of the same order) to the capture's OTLP receiver. Ids and
 * timestamps change on every run, as in a real run.
 */
import { randomBytes } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { CaptureError, loadFixture, record } from "../../packages/core/src/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const engine = resolve(here, "../../../../bin/attest-engine");
const PLANTED_KEY = "sk-test-tsplanted0123456789abcdef";
const PLANTED_EMAIL = "lee.customer@example.com";
const hex = (n: number): string => randomBytes(n).toString("hex");

async function post(url: string, body: unknown, headers: Record<string, string> = {}): Promise<void> {
  const resp = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
  await resp.text();
  if (!resp.ok) throw new Error(`${url}: ${resp.status}`);
}

/** One model call and an INC-1042-shaped trace, configured from the environment. */
async function scriptedAgent(refunds: number): Promise<void> {
  await post(
    `${process.env["OPENAI_BASE_URL"]}/chat/completions`,
    { model: "gpt-test", messages: [{ role: "user", content: `refund O-5 for ${PLANTED_EMAIL}` }] },
    { Authorization: `Bearer ${process.env["OPENAI_API_KEY"]}` },
  );
  const traceId = hex(16);
  const root = hex(8);
  const start = BigInt(Date.now()) * 1_000_000n;
  const span = (id: string, parent: string | null, name: string, i: number, attrs: Record<string, string>, len = 1) => ({
    traceId,
    spanId: id,
    ...(parent ? { parentSpanId: parent } : {}),
    name,
    startTimeUnixNano: String(start + BigInt(i) * 1_000_000n),
    endTimeUnixNano: String(start + BigInt(i + len) * 1_000_000n),
    attributes: Object.entries(attrs).map(([key, v]) => ({ key, value: { stringValue: v } })),
  });
  const tool = (name: string, args: string, i: number) =>
    span(hex(8), root, `execute_tool ${name}`, i, {
      "gen_ai.operation.name": "execute_tool",
      "gen_ai.tool.name": name,
      "gen_ai.tool.call.id": `call_${hex(6)}`,
      "gen_ai.tool.call.arguments": args,
      "gen_ai.tool.call.result": '{"ok":true}',
    });
  const spans = [
    span(root, null, "invoke_agent refund-desk", 0, { "gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "refund-desk" }, 20),
    span(hex(8), root, "chat gpt-test", 1, { "gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-test" }),
    tool("lookup_order", '{"order_id":"O-5"}', 2),
  ];
  for (let i = 0; i < refunds; i++) spans.push(tool("issue_refund", `{"order_id":"O-5","to":"${PLANTED_EMAIL}"}`, 4 + 2 * i));
  await post(process.env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] ?? "", { resourceSpans: [{ scopeSpans: [{ spans }] }] });
}

describe.skipIf(!existsSync(engine))("record() (real engine)", () => {
  let server: Server;
  const saved = { ...process.env };

  beforeAll(async () => {
    server = createServer((req, res) => {
      req.resume();
      req.on("end", () => {
        if (req.headers.authorization !== `Bearer ${PLANTED_KEY}`) {
          res.writeHead(401).end();
          return;
        }
        res.writeHead(200, { "Content-Type": "application/json" }).end(
          JSON.stringify({
            id: `chatcmpl-${hex(8)}`,
            object: "chat.completion",
            created: Date.now(),
            model: "gpt-test",
            choices: [{ index: 0, finish_reason: "stop", message: { role: "assistant", content: "ok" } }],
          }),
        );
      });
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    const port = (server.address() as { port: number }).port;
    Object.assign(process.env, {
      OPENAI_BASE_URL: `http://127.0.0.1:${port}/v1`,
      OPENAI_API_KEY: PLANTED_KEY,
      ATTEST_ENGINE_PATH: engine,
      ATTEST_ENGINE_NO_DOWNLOAD: "1",
    });
  });

  afterAll(() => {
    server.close();
    for (const k of ["OPENAI_BASE_URL", "OPENAI_API_KEY", "ATTEST_ENGINE_PATH", "ATTEST_ENGINE_NO_DOWNLOAD"]) {
      if (saved[k] === undefined) delete process.env[k];
      else process.env[k] = saved[k];
    }
  });

  it("captures the incident twice into byte-identical, redacted, verified fixtures", async () => {
    const upstream = process.env["OPENAI_BASE_URL"];
    const fixtures: string[] = [];
    for (const run of ["a", "b"]) {
      const dir = mkdtempSync(join(tmpdir(), `attest-record-${run}-`));
      const result = await record(
        "inc-5",
        async (capture) => {
          expect(process.env["OPENAI_BASE_URL"]).toBe(capture.env["OPENAI_BASE_URL"]);
          expect(process.env["OPENAI_BASE_URL"]).not.toBe(upstream);
          await scriptedAgent(2);
          return run;
        },
        { outDir: join(dir, "tests", "attest", "fixtures"), expectFail: true },
      );
      expect(process.env["OPENAI_BASE_URL"]).toBe(upstream);
      expect(result.value).toBe(run);
      expect(result.verified).toBe(true);
      expect(result.testFile).toBe(join(dir, "tests", "attest", "inc-5.test.ts"));
      fixtures.push(result.fixture);
    }

    const [first, second] = fixtures.map((f) => readFileSync(f, "utf8"));
    expect(first).toBe(second);
    expect(first).not.toContain(PLANTED_KEY);
    expect(first).not.toContain(PLANTED_EMAIL);
    const fixture = loadFixture(fixtures[0] ?? "");
    expect(fixture.source.kind).toBe("in_process");
    expect(fixture.verification.expected_failures).toEqual(["cardinality-issue_refund-per-order_id"]);
    expect(fixture.exchanges.map((e) => e["api"])).toEqual(["chat_completions"]);
  }, 30_000);

  it("drafts the observed per-id maximum and verifies a double refund without expectFail", async () => {
    const dir = mkdtempSync(join(tmpdir(), "attest-record-"));

    const result = await record("inc-5", () => scriptedAgent(2), { outDir: join(dir, "fixtures") });

    const refund = loadFixture(result.fixture).contracts.find((c) => c.id === "cardinality-issue_refund-per-order_id");
    expect(result.verified).toBe(true);
    expect(refund?.spec["max"]).toBe(2);
  }, 30_000);

  it("throws CaptureError and drafts no test when expectFail finds no incident", async () => {
    const dir = mkdtempSync(join(tmpdir(), "attest-record-"));

    const err = await record("inc-5", () => scriptedAgent(1), { outDir: join(dir, "fixtures"), expectFail: true }).catch(
      (e: unknown) => e,
    );

    expect(err).toBeInstanceOf(CaptureError);
    expect((err as CaptureError).message).toMatch(/is not verified; no test drafted/);
    expect(loadFixture(join(dir, "fixtures", "inc-5.attest.json")).verification.verified).toBe(false);
    expect(existsSync(join(dir, "inc-5.test.ts"))).toBe(false);
  }, 30_000);

  it("abandons the capture and restores the environment when the callback throws", async () => {
    const dir = mkdtempSync(join(tmpdir(), "attest-record-"));
    const upstream = process.env["OPENAI_BASE_URL"];

    await expect(
      record("broken", () => {
        throw new Error("agent crashed");
      }, { outDir: dir }),
    ).rejects.toThrow("agent crashed");

    expect(process.env["OPENAI_BASE_URL"]).toBe(upstream);
    expect(existsSync(join(dir, "broken.attest.json"))).toBe(false);
  }, 30_000);
});
