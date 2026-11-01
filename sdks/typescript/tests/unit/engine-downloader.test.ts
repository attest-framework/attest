import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("node:os", async () => {
  const actual = await vi.importActual<typeof import("node:os")>("node:os");
  return {
    ...actual,
    platform: vi.fn(() => "darwin"),
    arch: vi.fn(() => "arm64"),
    homedir: actual.homedir,
  };
});

import * as os from "node:os";
import { platformKey, parseChecksums } from "../../packages/core/src/engine-downloader.js";

describe("platformKey", () => {
  beforeEach(() => {
    vi.mocked(os.platform).mockReturnValue("darwin");
    vi.mocked(os.arch).mockReturnValue("arm64");
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns darwin-arm64 for darwin/arm64", () => {
    vi.mocked(os.platform).mockReturnValue("darwin");
    vi.mocked(os.arch).mockReturnValue("arm64");
    expect(platformKey()).toBe("darwin-arm64");
  });

  it("returns darwin-amd64 for darwin/x64", () => {
    vi.mocked(os.platform).mockReturnValue("darwin");
    vi.mocked(os.arch).mockReturnValue("x64");
    expect(platformKey()).toBe("darwin-amd64");
  });

  it("returns linux-arm64 for linux/arm64", () => {
    vi.mocked(os.platform).mockReturnValue("linux");
    vi.mocked(os.arch).mockReturnValue("arm64");
    expect(platformKey()).toBe("linux-arm64");
  });

  it("returns linux-amd64 for linux/x64", () => {
    vi.mocked(os.platform).mockReturnValue("linux");
    vi.mocked(os.arch).mockReturnValue("x64");
    expect(platformKey()).toBe("linux-amd64");
  });

  it("returns windows-amd64 for win32/x64", () => {
    vi.mocked(os.platform).mockReturnValue("win32");
    vi.mocked(os.arch).mockReturnValue("x64");
    expect(platformKey()).toBe("windows-amd64");
  });

  it("returns windows-arm64 for win32/arm64", () => {
    vi.mocked(os.platform).mockReturnValue("win32");
    vi.mocked(os.arch).mockReturnValue("arm64");
    expect(platformKey()).toBe("windows-arm64");
  });

  it("throws for unsupported platform", () => {
    vi.mocked(os.platform).mockReturnValue("freebsd" as NodeJS.Platform);
    vi.mocked(os.arch).mockReturnValue("x64");
    expect(() => platformKey()).toThrow("Unsupported platform: freebsd");
  });

  it("throws for unsupported architecture", () => {
    vi.mocked(os.platform).mockReturnValue("linux");
    vi.mocked(os.arch).mockReturnValue("ia32");
    expect(() => platformKey()).toThrow("Unsupported architecture 'ia32'");
  });
});

describe("parseChecksums", () => {
  it("parses standard checksum format", () => {
    const hash1 = "a".repeat(64);
    const hash2 = "b".repeat(64);
    const text = [
      `${hash1}  attest-engine-darwin-arm64`,
      `${hash2}  attest-engine-linux-amd64`,
      "",
    ].join("\n");

    const result = parseChecksums(text);
    expect(result.size).toBe(2);
    expect(result.get("attest-engine-darwin-arm64")).toBe(hash1);
    expect(result.get("attest-engine-linux-amd64")).toBe(hash2);
  });

  it("skips empty lines", () => {
    const text = "\n\n  \n";
    const result = parseChecksums(text);
    expect(result.size).toBe(0);
  });
});
