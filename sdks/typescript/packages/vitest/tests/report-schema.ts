// Shared by the tests that validate written reports against report-v2.
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020 } from "ajv/dist/2020.js";

export const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../..");
export const engine = resolve(repoRoot, "bin/attest-engine");

export function reportV2Validator() {
  const schema = JSON.parse(
    readFileSync(join(repoRoot, "proto/attest/v1/report-v2.schema.json"), "utf8"),
  ) as Record<string, unknown>;
  const ajv = new Ajv2020({ strict: false });
  ajv.addFormat("date-time", /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/);
  return ajv.compile(schema);
}
