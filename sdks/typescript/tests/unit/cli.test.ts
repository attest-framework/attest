/**
 * T14 — TypeScript CLI unit tests.
 *
 * Tests the CLI commands: --version, cache stats, cache clear, init, validate.
 * Uses tmp directories and mocks process.exit/stdout/stderr.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import { main } from "../../packages/core/src/cli.js";

function makeTmpDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), "attest-cli-test-"));
}

function cleanTmpDir(dir: string): void {
  fs.rmSync(dir, { recursive: true, force: true });
}

describe("CLI --version", () => {
  it("prints version and exits 0", () => {
    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["--version"])).toThrow("EXIT");

    const output = writeSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("attest ");
    expect(exitSpy).toHaveBeenCalledWith(0);

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI cache stats", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = makeTmpDir();
  });

  afterEach(() => {
    cleanTmpDir(tmpDir);
    delete process.env["ATTEST_CACHE_DIR"];
  });

  it("reports no cache when DB does not exist", () => {
    process.env["ATTEST_CACHE_DIR"] = tmpDir;

    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["cache", "stats"])).toThrow("EXIT");

    const output = writeSpy.mock.calls.map((c) => c[0]).join("");
    const data = JSON.parse(output.trim());
    expect(data.exists).toBe(false);
    expect(data.file_size).toBe(0);
    expect(exitSpy).toHaveBeenCalledWith(0);

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("reports cache size when DB exists", () => {
    process.env["ATTEST_CACHE_DIR"] = tmpDir;
    const dbPath = path.join(tmpDir, "attest.db");
    fs.writeFileSync(dbPath, "fake sqlite data".repeat(5));

    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["cache", "stats"])).toThrow("EXIT");

    const output = writeSpy.mock.calls.map((c) => c[0]).join("");
    const data = JSON.parse(output.trim());
    expect(data.exists).toBe(true);
    expect(data.file_size).toBeGreaterThan(0);
    expect(data.path).toContain("attest.db");

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI cache clear", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = makeTmpDir();
  });

  afterEach(() => {
    cleanTmpDir(tmpDir);
    delete process.env["ATTEST_CACHE_DIR"];
  });

  it("removes the DB file", () => {
    process.env["ATTEST_CACHE_DIR"] = tmpDir;
    const dbPath = path.join(tmpDir, "attest.db");
    fs.writeFileSync(dbPath, "test data");

    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["cache", "clear"])).toThrow("EXIT");
    expect(fs.existsSync(dbPath)).toBe(false);

    const output = writeSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("Cleared cache");

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("handles missing DB gracefully", () => {
    process.env["ATTEST_CACHE_DIR"] = tmpDir;

    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["cache", "clear"])).toThrow("EXIT");

    const output = writeSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("No cache to clear");

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI cache unknown subcommand", () => {
  it("exits with error for unknown cache subcommand", () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["cache", "foobar"])).toThrow("EXIT");

    const output = stderrSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("Unknown cache subcommand");
    expect(exitSpy).toHaveBeenCalledWith(1);

    stderrSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI init", () => {
  let tmpDir: string;
  let originalCwd: string;

  beforeEach(() => {
    tmpDir = makeTmpDir();
    originalCwd = process.cwd();
    process.chdir(tmpDir);
  });

  afterEach(() => {
    process.chdir(originalCwd);
    cleanTmpDir(tmpDir);
  });

  it("creates tests directory and sample test file", () => {
    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["init"])).toThrow("EXIT");

    const testsDir = path.join(tmpDir, "tests");
    expect(fs.existsSync(testsDir)).toBe(true);
    expect(fs.existsSync(path.join(testsDir, "test_my_agent.test.ts"))).toBe(true);

    const content = fs.readFileSync(
      path.join(testsDir, "test_my_agent.test.ts"),
      "utf-8",
    );
    expect(content).toContain("@attest-ai/core");

    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("does not overwrite existing test file", () => {
    const testsDir = path.join(tmpDir, "tests");
    fs.mkdirSync(testsDir, { recursive: true });
    const testFile = path.join(testsDir, "test_my_agent.test.ts");
    fs.writeFileSync(testFile, "// custom test\n");

    const writeSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["init"])).toThrow("EXIT");

    expect(fs.readFileSync(testFile, "utf-8")).toBe("// custom test\n");

    stderrSpy.mockRestore();
    writeSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI validate", () => {
  let tmpDir: string;
  let originalCwd: string;

  beforeEach(() => {
    tmpDir = makeTmpDir();
    originalCwd = process.cwd();
    process.chdir(tmpDir);
  });

  afterEach(() => {
    process.chdir(originalCwd);
    cleanTmpDir(tmpDir);
  });

  it("warns when no test files exist", () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const stdoutSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["validate"])).toThrow("EXIT");

    const errOutput = stderrSpy.mock.calls.map((c) => c[0]).join("");
    expect(errOutput).toContain("Warning");
    expect(errOutput).toContain("no test files");

    stderrSpy.mockRestore();
    stdoutSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("reports found test files", () => {
    const testsDir = path.join(tmpDir, "tests");
    fs.mkdirSync(testsDir, { recursive: true });
    fs.writeFileSync(path.join(testsDir, "example.test.ts"), "// test\n");

    const stdoutSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["validate"])).toThrow("EXIT");

    const output = stdoutSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("Found 1 test file");

    stdoutSpy.mockRestore();
    stderrSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("warns on stale golden traces", () => {
    const testsDir = path.join(tmpDir, "tests");
    fs.mkdirSync(testsDir, { recursive: true });
    fs.writeFileSync(path.join(testsDir, "example.test.ts"), "// test\n");

    const tracesDir = path.join(tmpDir, "traces");
    fs.mkdirSync(tracesDir, { recursive: true });
    const goldenPath = path.join(tracesDir, "agent.golden");
    fs.writeFileSync(goldenPath, "trace data\n");
    // Set mtime to 31 days ago
    const staleTime = Date.now() / 1000 - 31 * 24 * 60 * 60;
    fs.utimesSync(goldenPath, staleTime, staleTime);

    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const stdoutSpy = vi.spyOn(process.stdout, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["validate"])).toThrow("EXIT");

    const errOutput = stderrSpy.mock.calls.map((c) => c[0]).join("");
    expect(errOutput).toContain("golden");
    expect(errOutput).toContain("Warning");

    stderrSpy.mockRestore();
    stdoutSpy.mockRestore();
    exitSpy.mockRestore();
  });
});

describe("CLI usage", () => {
  it("prints usage on no args and exits 0", () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main([])).toThrow("EXIT");

    const output = stderrSpy.mock.calls.map((c) => c[0]).join("");
    expect(output).toContain("Usage:");
    expect(exitSpy).toHaveBeenCalledWith(0);

    stderrSpy.mockRestore();
    exitSpy.mockRestore();
  });

  it("prints usage on unknown command and exits 1", () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockReturnValue(true);
    const exitSpy = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("EXIT");
    }) as never);

    expect(() => main(["unknown"])).toThrow("EXIT");
    expect(exitSpy).toHaveBeenCalledWith(1);

    stderrSpy.mockRestore();
    exitSpy.mockRestore();
  });
});
