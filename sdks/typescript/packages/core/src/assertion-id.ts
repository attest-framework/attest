import { createHash } from "node:crypto";

/**
 * Assertion ids, id scheme 2 (contract C3). Normative text:
 * proto/attest/v1/protocol-spec.md, "Assertion ids (id scheme 2)"; executable form:
 * protocol-tests/ids/assertion-ids.json.
 */

export const ID_SCHEME = 2;

/** What an id is computed for; names the assertion in rejection errors. */
export interface AssertionIdentity {
  readonly scopeId: string;
  readonly type: string;
  readonly spec: unknown;
}

function lonelySurrogateAt(text: string): number {
  for (let i = 0; i < text.length; i++) {
    const unit = text.charCodeAt(i);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = text.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        i++;
        continue;
      }
      return i;
    }
    if (unit >= 0xdc00 && unit <= 0xdfff) return i;
  }
  return -1;
}

function describe(identity: AssertionIdentity | undefined): string {
  if (identity === undefined) return "";
  return ` (assertion type ${JSON.stringify(identity.type)}, scope ${JSON.stringify(identity.scopeId)})`;
}

function jcsString(text: string, where: string, identity: AssertionIdentity | undefined): string {
  const at = lonelySurrogateAt(text);
  if (at >= 0) {
    throw new Error(
      `assertion id: ${where} contains an unpaired surrogate at UTF-16 index ${at}; ` +
        `it has no UTF-8 form${describe(identity)}`,
    );
  }
  // JSON.stringify escapes only `"`, `\`, and control characters below U+0020
  // (lowercase \u00xx), which is the RFC 8785 string form.
  return JSON.stringify(text);
}

function jcs(value: unknown, where: string, identity: AssertionIdentity | undefined): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "string":
      return jcsString(value, where, identity);
    case "boolean":
      return value ? "true" : "false";
    case "number":
      if (!Number.isFinite(value)) {
        throw new Error(`assertion id: ${where} is ${String(value)}, which JSON cannot represent${describe(identity)}`);
      }
      // ECMAScript Number::toString, with -0 serialized as 0, as RFC 8785 requires.
      return String(value);
    case "object": {
      if (Array.isArray(value)) {
        return `[${value
          .map((item, i) => jcs(item === undefined ? null : item, `${where}[${i}]`, identity))
          .join(",")}]`;
      }
      const record = value as Record<string, unknown>;
      // Default sort compares UTF-16 code units, the RFC 8785 key order.
      const keys = Object.keys(record)
        .filter((k) => record[k] !== undefined)
        .sort();
      const members = keys.map((key) => {
        const quoted = jcsString(key, `${where} key ${JSON.stringify(key)}`, identity);
        return `${quoted}:${jcs(record[key], `${where}.${key}`, identity)}`;
      });
      return `{${members.join(",")}}`;
    }
    default:
      throw new Error(`assertion id: ${where} has non-JSON type ${typeof value}${describe(identity)}`);
  }
}

/** RFC 8785 (JCS) serialization of an assertion spec; no spec is `{}`. */
export function canonicalSpec(spec: unknown, identity?: AssertionIdentity): string {
  return jcs(spec ?? {}, "spec", identity);
}

/** The exact bytes (as text) that are hashed: the JCS array `[scope_id, type, spec, occurrence_index]`. */
export function canonicalIdInput(
  scopeId: string,
  type: string,
  spec: unknown,
  occurrenceIndex: number,
): string {
  const identity: AssertionIdentity = { scopeId, type, spec };
  if (!Number.isInteger(occurrenceIndex) || occurrenceIndex < 0) {
    throw new Error(`assertion id: occurrence index must be an integer >= 0, got ${String(occurrenceIndex)}`);
  }
  return `[${jcsString(scopeId, "scope_id", identity)},${jcsString(type, "assertion_type", identity)},${canonicalSpec(spec, identity)},${occurrenceIndex}]`;
}

/** `a2_` plus the first 32 hex characters of SHA-256 over the UTF-8 bytes of the id input. */
export function assertionIdOf(
  scopeId: string,
  type: string,
  spec: unknown,
  occurrenceIndex: number,
): string {
  const input = canonicalIdInput(scopeId, type, spec, occurrenceIndex);
  return `a2_${createHash("sha256").update(input, "utf8").digest("hex").slice(0, 32)}`;
}

/**
 * Assigns ids inside one scope. The occurrence index of an assertion is the number of earlier
 * assertions given an id here with the same (type, canonical spec), in call order.
 */
export class AssertionIdAssigner {
  private readonly seen = new Map<string, number>();

  constructor(readonly scopeId: string) {
    if (scopeId === "") throw new Error("assertion id: scope id must not be empty");
  }

  next(type: string, spec: unknown): string {
    const key = `${jcsString(type, "assertion_type", { scopeId: this.scopeId, type, spec })}\u0000${canonicalSpec(spec, { scopeId: this.scopeId, type, spec })}`;
    const occurrence = this.seen.get(key) ?? 0;
    const id = assertionIdOf(this.scopeId, type, spec, occurrence);
    this.seen.set(key, occurrence + 1);
    return id;
  }
}
