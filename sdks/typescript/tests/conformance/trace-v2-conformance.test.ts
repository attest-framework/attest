/**
 * Cross-SDK trace-v2 fixtures (protocol-tests/trace-v2) against
 * proto/attest/v1/trace-v2.schema.json, and OTelAdapter against the engine built by
 * `make engine`: it must return exactly the golden traces in protocol-tests/trace-v2/otlp,
 * which the Go engine and the Python SDK assert too, and reject every malformed input.
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { OTelAdapter } from "../../packages/core/src/adapters/otel.js";
import { OTLPConversionError } from "../../packages/core/src/index.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixtures = join(root, "protocol-tests", "trace-v2");
const schema = JSON.parse(
  readFileSync(join(root, "proto", "attest", "v1", "trace-v2.schema.json"), "utf8"),
) as Record<string, unknown>;
const validate = new Ajv2020({ strict: false }).compile(schema);

function files(dir: string, suffix: string): string[] {
  return readdirSync(join(fixtures, dir))
    .filter((f) => f.endsWith(suffix))
    .sort()
    .map((f) => join(fixtures, dir, f));
}

const load = (path: string): unknown => JSON.parse(readFileSync(path, "utf8"));
const name = (path: string): string => path.slice(path.lastIndexOf("/") + 1);

const valid = files("valid", ".json");
const invalid = files("invalid", ".json");
const otlp = files("otlp", ".otlp.json");
const golden = files("otlp", ".trace.json");
const malformed = files("otlp-invalid", ".json");

describe("trace-v2 fixtures", () => {
  it("are present", () => {
    expect(valid.length && invalid.length && otlp.length && malformed.length).toBeGreaterThan(0);
  });

  it.each([...valid, ...golden].map((p) => [name(p), p]))("%s matches the schema", (_n, p) => {
    expect(validate(load(p)), JSON.stringify(validate.errors)).toBe(true);
  });

  it.each(invalid.map((p) => [name(p), p]))("invalid/%s is rejected by the schema", (_n, p) => {
    expect(validate(load(p))).toBe(false);
  });
});

const enginePath = join(root, "bin", "attest-engine");
const engineBuilt = existsSync(enginePath);

describe.skipIf(!engineBuilt)("OTelAdapter with the engine", () => {
  const saved = process.env["ATTEST_ENGINE_PATH"];
  beforeAll(() => {
    process.env["ATTEST_ENGINE_PATH"] = enginePath;
  });
  afterAll(() => {
    if (saved === undefined) delete process.env["ATTEST_ENGINE_PATH"];
    else process.env["ATTEST_ENGINE_PATH"] = saved;
  });

  it.each(otlp.map((p) => [name(p), p]))("%s converts to its golden trace", async (_n, p) => {
    const trace = await OTelAdapter.fromOtlp(readFileSync(p, "utf8"));
    expect(trace).toEqual(load(p.replace(".otlp.json", ".trace.json")));
  });

  it.each(malformed.map((p) => [name(p), p]))("otlp-invalid/%s is rejected", async (_n, p) => {
    await expect(OTelAdapter.fromOtlp(readFileSync(p))).rejects.toThrow(OTLPConversionError);
  });
});
