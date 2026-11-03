/**
 * CLI `--json` documents (proto/attest/v1/cli/<stem>.schema.json) against their fixtures.
 * Every directory protocol-tests/cli/<stem>/ pairs with cli/<stem>.schema.json: each
 * valid/*.json validates and each invalid/*.json is rejected. The same fixtures run in the
 * Python SDK and the Go engine.
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const fixturesDir = join(root, "protocol-tests", "cli");
const schemasDir = join(root, "proto", "attest", "v1", "cli");
const stems = readdirSync(fixturesDir, { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name)
  .sort();

function fixtures(stem: string, kind: "valid" | "invalid"): [string, unknown][] {
  const dir = join(fixturesDir, stem, kind);
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((f) => f.endsWith(".json"))
    .sort()
    .map((f) => [f, JSON.parse(readFileSync(join(dir, f), "utf8")) as unknown]);
}

function validator(stem: string) {
  const schema = JSON.parse(
    readFileSync(join(schemasDir, `${stem}.schema.json`), "utf8"),
  ) as Record<string, unknown>;
  return new Ajv2020({ strict: false }).compile(schema);
}

it("cli fixture directories include init", () => {
  expect(stems).toContain("init");
});

describe.each(stems)("cli/%s fixtures", (stem) => {
  const valid = fixtures(stem, "valid");
  const invalid = fixtures(stem, "invalid");

  it("have a schema and at least one valid and one invalid fixture", () => {
    expect(existsSync(join(schemasDir, `${stem}.schema.json`))).toBe(true);
    expect(valid.length).toBeGreaterThan(0);
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(valid)("valid/%s matches the schema", (_name, doc) => {
    const validate = validator(stem);
    expect(validate(doc), JSON.stringify(validate.errors)).toBe(true);
  });

  it.each(invalid)("invalid/%s is rejected by the schema", (_name, doc) => {
    expect(validator(stem)(doc)).toBe(false);
  });
});
