import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  CaptureError,
  attestExpect,
  dslCall,
  emitVitest,
  loadFixture,
  renderVitest,
  testPathFor,
  type Contract,
  type ExpectChain,
} from "../../packages/core/src/index.js";
import { captureWritesFixture } from "../../packages/core/src/cli.js";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
const validDir = join(root, "protocol-tests", "fixture-v1", "valid");
const validFixtures = readdirSync(validDir).filter((f) => f.endsWith(".attest.json")).sort();
const incident = join(validDir, "002-otlp-file-trace-only.attest.json");

const contract = (type: string, spec: Record<string, unknown>): Contract => ({
  id: "c",
  kind: "k",
  description: "d",
  type,
  spec,
});

describe("dslCall", () => {
  it.each([
    ["trace", { check: "trajectory", mode: "superset", tools: ["a", "b"], soft: false }, '.trajectory(["a", "b"], { mode: "superset" })'],
    ["trace", { check: "trajectory", mode: "strict", tools: ["a"] }, '.trajectory(["a"])'],
    ["trace", { check: "tool_order", before: "a", after: "b", soft: true }, '.toolCalledBefore("a", "b", { soft: true })'],
    [
      "trace",
      { check: "tool_cardinality", tool: "t", per: "order_id", max: 1, soft: false },
      '.toolCardinality("t", { per: "order_id", atMost: 1 })',
    ],
    ["trace", { check: "tool_cardinality", tool: "t", min: 1, max: 2 }, '.toolCardinality("t", { atLeast: 1, atMost: 2 })'],
    [
      "schema",
      { target: "output.structured", schema: { type: "object", required: ["a"] } },
      '.outputMatchesSchema({ type: "object", required: ["a"] })',
    ],
    ["constraint", { field: "metadata.cost_usd", operator: "lte", value: 0.0185, soft: false }, ".costUnder(0.0185)"],
  ])("renders a %s spec as its DSL call", (type, spec, call) => {
    expect(dslCall(contract(type, spec))).toBe(call);
  });

  it.each([
    ["trace", { check: "trajectory", mode: "partial_order", order: [["a", "b"]] }],
    ["constraint", { field: "metadata.latency_ms", operator: "lte", value: 1 }],
    ["llm_judge", {}],
    ["trace", { check: "tool_order", before: "a" }],
  ])("throws for an unsupported or malformed %s spec", (type, spec) => {
    expect(() => dslCall(contract(type, spec))).toThrow(/contract c/);
  });

  it.each(validFixtures)("renders calls that emit the drafted specs of %s", (file) => {
    const fixture = loadFixture(join(validDir, file));
    let chain: ExpectChain = attestExpect(fixture.trace);
    for (const c of fixture.contracts) {
      chain = new Function("chain", `return chain${dslCall(c)};`)(chain) as ExpectChain;
    }

    expect(chain.assertions.map((a) => a.spec)).toEqual(fixture.contracts.map((c) => c.spec));
    expect(chain.assertions.map((a) => a.type)).toEqual(fixture.contracts.map((c) => c.type));
  });
});

describe("renderVitest", () => {
  it("asserts the incident's expected statuses with inspect", () => {
    const dir = "/repo/tests/attest";
    const source = renderVitest(loadFixture(incident), join(dir, "fixtures", "inc-1042.attest.json"), join(dir, "inc-1042.test.ts"));

    expect(source).toContain('loadFixture(new URL("./fixtures/inc-1042.attest.json", import.meta.url))');
    expect(source).toContain('.toolCardinality("issue_refund", { per: "order_id", atMost: 1 })');
    expect(source).toContain('["cardinality-issue_refund-per-order_id", "hard_fail"],');
    expect(source).toContain('import { inspect } from "@attest-ai/vitest";');
    expect(source).toContain("await inspect(contracts())");
  });

  it("enforces a verified fixture's contracts with evaluate", () => {
    const fixture = loadFixture(incident);
    fixture.verification = { ...fixture.verification, expect_fail: false, expected_failures: [] };

    const source = renderVitest(fixture, "/r/fixtures/f.attest.json", "/r/f.test.ts");

    expect(source).toContain("await evaluate(contracts());");
    expect(source).not.toContain("EXPECTED");
    expect(source).toContain('import { describe, it } from "vitest";');
  });
});

describe("renderVitest with hostile captured text", () => {
  it("keeps a newline in a description inside the comment", () => {
    const fixture = loadFixture(incident);
    fixture.contracts = [
      {
        id: "tools-called",
        kind: "tools",
        description: "calls x\nprocess.exit(1)",
        type: "trace",
        spec: { check: "trajectory", mode: "superset", tools: ["x"], soft: false },
      },
    ];

    const source = renderVitest(fixture, "/r/fixtures/f.attest.json", "/r/f.test.ts");

    expect(source).toContain("// tools-called: calls x process.exit(1)");
    expect(source.split("\n").filter((l) => l.trim().startsWith("process.exit"))).toEqual([]);
  });

  it("emits a schema property named __proto__ as a computed key", () => {
    const call = dslCall(
      contract("schema", { target: "output.structured", schema: JSON.parse('{"properties":{"__proto__":{"type":"string"}}}') }),
    );
    const chain = new Function("chain", `return chain${call};`)(attestExpect({ trace_id: "t", output: {}, steps: [] } as never)) as ExpectChain;

    expect(call).toContain('["__proto__"]: { type: "string" }');
    expect(Object.keys((chain.assertions[0]?.spec as { schema: { properties: object } }).schema.properties)).toEqual(["__proto__"]);
  });
});

describe("emitVitest", () => {
  it("writes the test once and leaves an edited test unchanged", () => {
    const dir = mkdtempSync(join(tmpdir(), "attest-emit-"));
    const fixture = join(dir, "tests", "attest", "fixtures", "inc-1042.attest.json");
    mkdirSync(dirname(fixture), { recursive: true });
    copyFileSync(incident, fixture);

    const first = emitVitest(fixture);
    expect(first).toBe(join(dir, "tests", "attest", "inc-1042.test.ts"));
    writeFileSync(first ?? "", "// edited\n");
    const second = emitVitest(fixture);

    expect(second).toBeNull();
    expect(readFileSync(first ?? "", "utf8")).toBe("// edited\n");
  });

  it("names the test after the fixture", () => {
    expect(testPathFor(join("tests", "attest", "fixtures", "inc-1042.attest.json"))).toBe(
      join("tests", "attest", "inc-1042.test.ts"),
    );
  });
});

describe("captureWritesFixture", () => {
  it.each([
    [["--name", "x", "--", "make", "--help"], true],
    [["--name", "x", "--from", "a.json"], true],
    [["--check-redaction", "f.json"], false],
    [["--check-redaction=f.json"], false],
    [["--name", "x", "--serve"], false],
    [["--help"], false],
    [["-serve"], false],
    [["-check-redaction=f.json"], false],
  ])("%j → %s", (args, writes) => {
    expect(captureWritesFixture(args)).toBe(writes);
  });
});

describe("loadFixture validation", () => {
  interface Doc extends Record<string, unknown> {
    verification: Record<string, unknown>;
    contracts: Array<Record<string, unknown>>;
  }
  const cases: Array<[string, (d: Doc) => void]> = [
    ["docstring-style name", (d) => (d["name"] = 'x"""\nimport os')],
    ["newline + require name", (d) => (d["name"] = "a\nrequire('fs')")],
    ["no verification", (d) => delete d["verification"]],
    ["no world", (d) => delete d["world"]],
    ["contract without spec", (d) => delete d.contracts[0]?.["spec"]],
    ["results not an array", (d) => (d.verification["results"] = "nope")],
  ];
  it.each(cases)("rejects a fixture with %s", (_name, mutate) => {
    const doc = JSON.parse(readFileSync(incident, "utf8")) as Doc;
    mutate(doc);
    const path = join(mkdtempSync(join(tmpdir(), "attest-fixture-")), "f.attest.json");
    writeFileSync(path, JSON.stringify(doc));

    expect(() => loadFixture(path)).toThrow(CaptureError);
  });

  it("keeps a hostile name inside the header comment", () => {
    const fixture = loadFixture(incident);
    fixture.name = "x\nrequire('child_process').execSync('true')";

    const source = renderVitest(fixture, "/r/fixtures/f.attest.json", "/r/f.test.ts");

    expect(source.split("\n").filter((l) => l.startsWith("require("))).toEqual([]);
    expect(source.split("\n")[0]).toContain('fixture "x\\nrequire(');
  });
});

describe("emitVitest with a symbolic link at the target", () => {
  it.each([true, false])("refuses and writes nothing (dangling: %s)", (dangling) => {
    const dir = mkdtempSync(join(tmpdir(), "attest-emit-"));
    const fixture = join(dir, "tests", "attest", "fixtures", "inc-1042.attest.json");
    mkdirSync(dirname(fixture), { recursive: true });
    copyFileSync(incident, fixture);
    const victim = join(dir, "victim.txt");
    if (!dangling) writeFileSync(victim, "keep");
    symlinkSync(victim, join(dir, "tests", "attest", "inc-1042.test.ts"));

    expect(() => emitVitest(fixture)).toThrow(/symbolic link/);
    expect(existsSync(victim)).toBe(!dangling);
    if (!dangling) expect(readFileSync(victim, "utf8")).toBe("keep");
  });
});
