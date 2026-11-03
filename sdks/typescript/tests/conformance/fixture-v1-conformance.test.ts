/**
 * Cross-SDK fixture-v1 fixtures (protocol-tests/fixture-v1) against
 * proto/attest/v1/fixture-v1.schema.json, which embeds trace-v2.schema.json by reference.
 * The Go engine and the Python SDK run the same files. loadFixture reads every valid one.
 */
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { CaptureError, loadFixture } from "../../packages/core/src/index.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixtures = join(root, "protocol-tests", "fixture-v1");
const schemas = join(root, "proto", "attest", "v1");
const read = (p: string): Record<string, unknown> => JSON.parse(readFileSync(p, "utf8")) as Record<string, unknown>;

const ajv = new Ajv2020({ strict: false });
ajv.addSchema(read(join(schemas, "trace-v2.schema.json")));
const validate = ajv.compile(read(join(schemas, "fixture-v1.schema.json")));

function files(dir: string): string[] {
  return readdirSync(join(fixtures, dir))
    .filter((f) => f.endsWith(".attest.json"))
    .sort()
    .map((f) => join(fixtures, dir, f));
}
const name = (p: string): string => p.slice(p.lastIndexOf("/") + 1);
const valid = files("valid");
const invalid = files("invalid");

describe("fixture-v1 fixtures", () => {
  it("are present", () => {
    expect(valid.length && invalid.length).toBeGreaterThan(0);
  });

  it.each(valid.map((p) => [name(p), p]))("%s matches the schema", (_n, p) => {
    expect(validate(read(p)), JSON.stringify(validate.errors)).toBe(true);
  });

  it.each(invalid.map((p) => [name(p), p]))("invalid/%s is rejected by the schema", (_n, p) => {
    expect(validate(read(p))).toBe(false);
  });

  it.each(valid.map((p) => [name(p), p]))("loadFixture reads %s", (_n, p) => {
    const doc = read(p);
    const fixture = loadFixture(p);

    expect(fixture.name).toBe(doc["name"]);
    expect(fixture.trace).toEqual(doc["trace"]);
  });

  it("loadFixture rejects an OTLP export", () => {
    expect(() => loadFixture(join(root, "protocol-tests", "trace-v2", "otlp", "inc-1042-refund.otlp.json"))).toThrow(
      CaptureError,
    );
  });
});
