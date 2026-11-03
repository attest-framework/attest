// The scripted agent `attest capture` records in replay-e2e.test.ts: one model call
// through OPENAI_BASE_URL, then its spans to the OTLP/HTTP receiver.
import { randomBytes } from "node:crypto";

const system = process.env.REPLAY_SYSTEM ?? "You refund once.";
const res = await fetch(`${process.env.OPENAI_BASE_URL}/chat/completions`, {
  method: "POST",
  headers: { "content-type": "application/json", authorization: `Bearer ${process.env.OPENAI_API_KEY ?? "unused"}` },
  body: JSON.stringify({
    model: "gpt-test",
    messages: [
      { role: "system", content: system },
      { role: "user", content: "refund order O-7" },
    ],
  }),
});
if (!res.ok) throw new Error(`model call: ${res.status}`);
await res.arrayBuffer();

const traceId = randomBytes(16).toString("hex");
const root = randomBytes(8).toString("hex");
const start = Date.now() * 1e6;
const attr = (key, value) => ({ key, value: { stringValue: value } });
const span = (id, parent, name, i, attrs) => ({
  traceId,
  spanId: id,
  ...(parent === "" ? {} : { parentSpanId: parent }),
  name,
  startTimeUnixNano: String(start + i * 1e6),
  endTimeUnixNano: String(start + (i + 1) * 1e6),
  attributes: attrs,
});
const spans = [
  span(root, "", "invoke_agent refund-desk", 0, [attr("gen_ai.operation.name", "invoke_agent"), attr("gen_ai.agent.name", "refund-desk")]),
  span(randomBytes(8).toString("hex"), root, "chat gpt-test", 1, [attr("gen_ai.operation.name", "chat"), attr("gen_ai.request.model", "gpt-test")]),
  span(randomBytes(8).toString("hex"), root, "execute_tool lookup_order", 3, [
    attr("gen_ai.operation.name", "execute_tool"),
    attr("gen_ai.tool.name", "lookup_order"),
    attr("gen_ai.tool.call.id", `call_${randomBytes(6).toString("hex")}`),
    attr("gen_ai.tool.call.arguments", '{"order_id":"O-7"}'),
    attr("gen_ai.tool.call.result", '{"ok":true}'),
  ]),
];
const exported = await fetch(process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ resourceSpans: [{ scopeSpans: [{ spans }] }] }),
});
if (!exported.ok) throw new Error(`export: ${exported.status}`);
