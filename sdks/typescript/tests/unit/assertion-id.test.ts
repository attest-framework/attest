import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  AssertionIdAssigner,
  assertionIdOf,
  canonicalIdInput,
  canonicalSpec,
} from "../../packages/core/src/assertion-id.js";

const vectorFile = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../../../protocol-tests/ids/assertion-ids.json",
);

interface Vector {
  name: string;
  scope_id: string;
  assertion_type: string;
  spec_json: string;
  occurrence_index: number;
  canonical_spec: string;
  canonical_input: string;
  id: string;
  spec_hash?: string;
}

const doc = JSON.parse(readFileSync(vectorFile, "utf8")) as {
  vectors: Vector[];
  invalid?: Array<
    Partial<Vector> & { name: string; scope_id_bytes_hex?: string; spec_json_bytes_hex?: string }
  >;
};

const strictUtf8 = new TextDecoder("utf-8", { fatal: true });

function decodeHex(hex: string): string {
  return strictUtf8.decode(Buffer.from(hex, "hex"));
}

describe("assertion id scheme 2 golden vectors", () => {
  it("includes published effect evidence vectors and invalid vectors", () => {
    expect(doc.vectors).toHaveLength(40);
    expect(doc.invalid).toHaveLength(7);
  });

  it.each(doc.vectors.map((v) => [v.name, v] as const))("%s: canonical bytes and id", (_name, v) => {
    const spec: unknown = JSON.parse(v.spec_json);
    const canonical = canonicalSpec(spec, { scopeId: v.scope_id, type: v.assertion_type, spec });
    expect(canonical).toBe(v.canonical_spec);
    if (v.spec_hash !== undefined) {
      expect(createHash("sha256").update(canonical, "utf8").digest("hex")).toBe(v.spec_hash);
    }
    expect(canonicalIdInput(v.scope_id, v.assertion_type, spec, v.occurrence_index)).toBe(v.canonical_input);
    const id = assertionIdOf(v.scope_id, v.assertion_type, spec, v.occurrence_index);
    expect(id).toBe(v.id);
    expect(id).toMatch(/^a2_[0-9a-f]{32}$/);
  });
});

describe("assertion id rejection of text with no UTF-8 form", () => {
  const lone = "\ud800";
  it("rejects an unpaired surrogate in a spec value and names the assertion", () => {
    expect(() => assertionIdOf("t::a", "output_contains", { value: `x${lone}` }, 0)).toThrow(
      /unpaired surrogate.*assertion type "output_contains", scope "t::a"/,
    );
  });
  it("rejects an unpaired low surrogate in a spec key", () => {
    expect(() => assertionIdOf("t::a", "output_contains", { ["a\udc00"]: 1 }, 0)).toThrow(/unpaired surrogate/);
  });
  it("rejects an unpaired surrogate in the scope id and the assertion type", () => {
    expect(() => assertionIdOf(`t${lone}`, "output_contains", {}, 0)).toThrow(/scope_id/);
    expect(() => assertionIdOf("t", `x${lone}`, {}, 0)).toThrow(/assertion_type/);
  });
  it("accepts a well-formed surrogate pair", () => {
    expect(() => assertionIdOf("t", "output_contains", { value: "\u{1f600}" }, 0)).not.toThrow();
  });
  it.each((doc.invalid ?? []).map((v) => [v.name, v] as const))("invalid vector %s is rejected", (_name, v) => {
    // The id API takes JS strings, so bytes that are not UTF-8 are rejected at the
    // strict decode, before any id can be computed.
    expect(() => {
      const scope = v.scope_id_bytes_hex !== undefined ? decodeHex(v.scope_id_bytes_hex) : (v.scope_id ?? "t");
      const specText = v.spec_json_bytes_hex !== undefined ? decodeHex(v.spec_json_bytes_hex) : (v.spec_json ?? "{}");
      const spec: unknown = JSON.parse(specText);
      assertionIdOf(scope, v.assertion_type ?? "output_contains", spec, v.occurrence_index ?? 0);
    }).toThrow();
  });
});

describe("AssertionIdAssigner", () => {
  const a = { target: "output.message", check: "contains", value: "ok" };
  const b = { target: "output.message", check: "contains", value: "other" };

  it("gives two identical assertions distinct ids", () => {
    const ids = new AssertionIdAssigner("f.test.ts > t");
    const [first, second] = [ids.next("content", a), ids.next("content", a)];
    expect(first).not.toBe(second);
    expect(first).toBe(assertionIdOf("f.test.ts > t", "content", a, 0));
    expect(second).toBe(assertionIdOf("f.test.ts > t", "content", a, 1));
  });

  it("keeps every id when distinct assertions are reordered", () => {
    const forward = new AssertionIdAssigner("s");
    const fa = forward.next("content", a);
    const fb = forward.next("content", b);
    const backward = new AssertionIdAssigner("s");
    const bb = backward.next("content", b);
    const ba = backward.next("content", a);
    expect([fa, fb]).toEqual([ba, bb]);
  });

  it("gives a changed spec a new id", () => {
    const ids = new AssertionIdAssigner("s");
    expect(ids.next("content", a)).not.toBe(new AssertionIdAssigner("s").next("content", b));
  });

  it("treats key order and 1.0 versus 1 as the same spec", () => {
    const ids = new AssertionIdAssigner("s");
    const first = ids.next("cost", { max: 1.0, soft: false });
    const second = new AssertionIdAssigner("s").next("cost", { soft: false, max: 1 });
    expect(first).toBe(second);
  });

  it("counts effect occurrences by contract across different runtime worlds", () => {
    const spec = { check: "count", resource: "refunds", equals: 1, world: { refunds: [{}] } };
    const changed = { ...spec, world: { refunds: [{}, {}] } };
    const ids = new AssertionIdAssigner("s");
    expect(ids.next("effect", spec)).toBe(assertionIdOf("s", "effect", changed, 0));
    expect(ids.next("effect", changed)).toBe(assertionIdOf("s", "effect", spec, 1));
    expect(new AssertionIdAssigner("s").next("effect", changed)).toBe(assertionIdOf("s", "effect", spec, 0));
    expect(assertionIdOf("s", "effect", { ...changed, equals: 2 }, 0)).not.toBe(assertionIdOf("s", "effect", spec, 0));
    expect(spec.world.refunds).toHaveLength(1);
    expect(changed.world.refunds).toHaveLength(2);
  });

  it("preserves nested world and world on every other assertion type", () => {
    const spec = { where: { world: "one" }, world: { value: 1 } };
    const changed = { ...spec, world: { value: 2 } };
    expect(assertionIdOf("s", "content", spec, 0)).not.toBe(assertionIdOf("s", "content", changed, 0));
    expect(assertionIdOf("s", "effect", spec, 0)).not.toBe(
      assertionIdOf("s", "effect", { ...spec, where: { world: "two" } }, 0),
    );
    expect(assertionIdOf("s", "effect", { world: null }, 0)).toBe(assertionIdOf("s", "effect", {}, 0));
  });

  it("shares identity with replay fixture contracts without their runtime world", () => {
    // Replay contracts carry the type and spec separately, just like wire assertions.
    const replayContracts = doc.vectors.filter((v) => v.assertion_type === "effect");
    for (const contract of replayContracts) {
      const spec: unknown = JSON.parse(contract.spec_json);
      // Non-object vectors exercise identity encoding, not valid replay contracts.
      if (spec === null || typeof spec !== "object" || Array.isArray(spec)) continue;
      expect(assertionIdOf(contract.scope_id, contract.assertion_type, {
        ...spec, world: { resources: { refunds: [] }, events: [] },
      }, contract.occurrence_index)).toBe(contract.id);
    }
  });

  it("does not canonicalize excluded evidence but still validates identity fields", () => {
    const spec = { check: "count", world: { invalid: Number.NaN, text: "\ud800" } };
    expect(assertionIdOf("s", "effect", spec, 0)).toBe(assertionIdOf("s", "effect", { check: "count" }, 0));
    expect(() => assertionIdOf("s", "content", spec, 0)).toThrow();
    expect(() => assertionIdOf("s", "effect", { ...spec, where: { world: "\ud800" } }, 0)).toThrow();
  });

  it("does not consume an occurrence when the assertion is rejected", () => {
    const ids = new AssertionIdAssigner("s");
    expect(() => ids.next("content", { value: "\ud800" })).toThrow();
    expect(ids.next("content", a)).toBe(assertionIdOf("s", "content", a, 0));
  });

  it("rejects an empty scope", () => {
    expect(() => new AssertionIdAssigner("")).toThrow(/scope id/);
  });
});
