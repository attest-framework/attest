/**
 * Cross-SDK world-v1 fixtures (protocol-tests/world-v1) against
 * proto/attest/v1/world-v1.schema.json: definitions against the schema, snapshots against
 * its `snapshot` definition. The Go engine and the Python SDK run the same files.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const schema = JSON.parse(
  readFileSync(join(root, "proto", "attest", "v1", "world-v1.schema.json"), "utf8"),
) as Record<string, unknown>;
const ajv = new Ajv2020({ strict: false });
const validateDefinition = ajv.compile(schema);
const validateSnapshot = ajv.compile({ $ref: "#/$defs/snapshot", $defs: schema["$defs"] });

function fixtures(kind: string): [string, unknown][] {
  const dir = join(root, "protocol-tests", "world-v1", kind);
  return readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => [f, JSON.parse(readFileSync(join(dir, f), "utf8")) as unknown]);
}

describe("world-v1 fixtures", () => {
  it("are present", () => {
    for (const kind of ["valid", "invalid", "snapshot-valid", "snapshot-invalid"]) {
      expect(fixtures(kind).length, kind).toBeGreaterThan(0);
    }
  });

  it.each(fixtures("valid"))("valid/%s matches the schema", (_name, doc) => {
    expect(validateDefinition(doc), JSON.stringify(validateDefinition.errors)).toBe(true);
  });

  it.each(fixtures("invalid"))("invalid/%s is rejected", (_name, doc) => {
    expect(validateDefinition(doc)).toBe(false);
  });

  it.each(fixtures("snapshot-valid"))("snapshot-valid/%s matches the snapshot definition", (_name, doc) => {
    expect(validateSnapshot(doc), JSON.stringify(validateSnapshot.errors)).toBe(true);
  });

  it.each(fixtures("snapshot-invalid"))("snapshot-invalid/%s is rejected", (_name, doc) => {
    expect(validateSnapshot(doc)).toBe(false);
  });
});
