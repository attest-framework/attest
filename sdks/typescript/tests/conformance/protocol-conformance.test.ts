/**
 * Cross-SDK protocol conformance suite, TypeScript runner.
 *
 * Replays each fixture in attest/protocol-tests/fixtures/ through the
 * real AttestClient line handler and asserts on the resulting protocol
 * diagnostics. The same fixtures power the Python conformance suite so
 * both SDKs stay observationally identical.
 */
import { EventEmitter } from "node:events";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, it, expect, vi } from "vitest";
import { AttestClient } from "../../packages/core/src/client.js";
import type { ProtocolDiagnosticKind } from "../../packages/core/src/proto/diagnostics.js";

interface ExpectedDiagnostic {
  readonly kind: ProtocolDiagnosticKind;
  readonly messageContains?: string;
}

interface ConformanceFixture {
  readonly name: string;
  readonly description: string;
  readonly lines: readonly string[];
  readonly prePending?: readonly number[];
  readonly expectedDiagnostics: readonly ExpectedDiagnostic[];
  readonly expectedDesync: boolean;
}

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const FIXTURES_DIR = resolve(__dirname, "..", "..", "..", "..", "protocol-tests", "fixtures");

function loadFixtures(): ConformanceFixture[] {
  const files = readdirSync(FIXTURES_DIR)
    .filter((f) => f.endsWith(".json"))
    .sort();
  return files.map((f) => {
    const raw = readFileSync(join(FIXTURES_DIR, f), "utf-8");
    return JSON.parse(raw) as ConformanceFixture;
  });
}

function makeFakeEngine(): { readlineInterface: EventEmitter; childProcess: null } {
  return {
    readlineInterface: new EventEmitter(),
    childProcess: null,
  };
}

const fixtures = loadFixtures();

describe("Protocol conformance suite", () => {
  for (const fixture of fixtures) {
    it(fixture.name, () => {
      const engine = makeFakeEngine();
      const client = new AttestClient(engine as never, {
        logger: { warn: vi.fn(), error: vi.fn() },
      });

      // Register pre-pending requests, capturing futures so we can drain
      // any rejections (vitest treats unhandled rejections as failures).
      const drains: Promise<unknown>[] = [];
      for (const id of fixture.prePending ?? []) {
        const p = new Promise<unknown>((resolve, reject) => {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (client as any).pending.set(id, { resolve, reject });
        }).catch(() => {
          /* drained */
        });
        drains.push(p);
      }

      for (const line of fixture.lines) {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (client as any).handleLine(line);
      }

      const diags = client.protocolDiagnostics();
      expect(diags.length, `${fixture.name}: diagnostic count`).toBe(
        fixture.expectedDiagnostics.length,
      );

      for (let i = 0; i < diags.length; i += 1) {
        const want = fixture.expectedDiagnostics[i]!;
        const got = diags[i]!;
        expect(got.kind, `${fixture.name}[${i}].kind`).toBe(want.kind);
        if (want.messageContains !== undefined) {
          expect(got.message, `${fixture.name}[${i}].message`).toContain(
            want.messageContains,
          );
        }
      }

      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const desynced = (client as any).desynced as boolean;
      expect(desynced, `${fixture.name}: desync flag`).toBe(fixture.expectedDesync);

      // Force pending request rejection so unhandled-promise warnings
      // do not bleed across tests.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const pending = (client as any).pending as Map<
        number,
        { reject: (e: unknown) => void }
      >;
      for (const entry of pending.values()) {
        entry.reject(new Error("conformance test cleanup"));
      }
      // Await drains in microtask flush so vitest doesn't warn.
      return Promise.allSettled(drains).then(() => undefined);
    });
  }
});
