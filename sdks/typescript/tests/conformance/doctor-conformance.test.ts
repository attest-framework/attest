/**
 * Cross-SDK doctor fixtures (protocol-tests/doctor) against proto/attest/v1/doctor.schema.json.
 * The same fixtures run in the Python SDK and the Go engine.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const schema = JSON.parse(
  readFileSync(join(root, "proto", "attest", "v1", "doctor.schema.json"), "utf8"),
) as Record<string, unknown>;
const validate = new Ajv2020({ strict: false }).compile(schema);

function fixtures(kind: "valid" | "invalid"): [string, unknown][] {
  const dir = join(root, "protocol-tests", "doctor", kind);
  return readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => [f, JSON.parse(readFileSync(join(dir, f), "utf8")) as unknown]);
}

const valid = fixtures("valid");
const invalid = fixtures("invalid");

describe("doctor fixtures", () => {
  it("are present", () => {
    expect(valid.length).toBeGreaterThan(0);
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(valid)("valid/%s matches the schema", (_name, doc) => {
    expect(validate(doc), JSON.stringify(validate.errors)).toBe(true);
  });

  it.each(invalid)("invalid/%s is rejected by the schema", (_name, doc) => {
    expect(validate(doc)).toBe(false);
  });
});
