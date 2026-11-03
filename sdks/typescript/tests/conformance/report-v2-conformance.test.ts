/**
 * Cross-SDK report-v2 fixtures (protocol-tests/report-v2) against
 * proto/attest/v1/report-v2.schema.json. The same fixtures run in the Python SDK
 * and the Go engine; the Vitest reporter's output is checked in
 * packages/vitest/tests/attest-reporter.test.ts.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const schema = JSON.parse(
  readFileSync(join(root, "proto", "attest", "v1", "report-v2.schema.json"), "utf8"),
) as Record<string, unknown>;
const ajv = new Ajv2020({ strict: false });
ajv.addFormat("date-time", /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/);
const validate = ajv.compile(schema);

function fixtures(kind: "valid" | "invalid"): [string, unknown][] {
  const dir = join(root, "protocol-tests", "report-v2", kind);
  return readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => [f, JSON.parse(readFileSync(join(dir, f), "utf8")) as unknown]);
}

const valid = fixtures("valid");
const invalid = fixtures("invalid");

describe("report-v2 fixtures", () => {
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
