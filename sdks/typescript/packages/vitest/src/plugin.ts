import {
  EngineManager,
  AttestClient,
  isSimulationMode,
  requireSimulationCapability,
  type AgentResult,
} from "@attest-ai/core";
import { MIN_PASS_RATE_ENV, validateMinPassRate } from "./trials.js";

let engineManager: EngineManager | undefined;
let attestClient: AttestClient | undefined;

export interface AttestGlobalSetupOptions {
  enginePath?: string;
  logLevel?: string;
  /**
   * Minimum passed / evaluated trials for every trials() test of the run (0..1,
   * default 1.0); trials(body, { minPassRate }) overrides it per test. Reaches
   * the workers as the ATTEST_MIN_PASS_RATE environment variable.
   */
  minPassRate?: number;
}

declare global {
  // eslint-disable-next-line no-var
  var __attest_engine__: EngineManager | undefined;
  // eslint-disable-next-line no-var
  var __attest_client__: AttestClient | undefined;
  // eslint-disable-next-line no-var
  var __attest_session_cost__: number;
  // eslint-disable-next-line no-var
  var __attest_session_soft_failures__: number;
  // eslint-disable-next-line no-var
  var __attest_session_failures__: AgentResult[] | undefined;
}

export function attestGlobalSetup(options?: AttestGlobalSetupOptions) {
  const minPassRate =
    options?.minPassRate === undefined
      ? undefined
      : validateMinPassRate(options.minPassRate, `minPassRate=${options.minPassRate}`);
  return {
    async setup() {
      if (minPassRate !== undefined) process.env[MIN_PASS_RATE_ENV] = String(minPassRate);
      globalThis.__attest_session_cost__ = 0;
      globalThis.__attest_session_soft_failures__ = 0;
      globalThis.__attest_session_failures__ = [];

      if (isSimulationMode()) {
        // The engine decides simulation at startup from its environment;
        // this also covers config({ simulation: true }).
        process.env["ATTEST_SIMULATION"] = "1";
      }

      engineManager = new EngineManager({
        enginePath: options?.enginePath,
        logLevel: options?.logLevel ?? "warn",
      });

      const init = await engineManager.start();
      if (isSimulationMode()) {
        try {
          requireSimulationCapability(init);
        } catch (err) {
          await engineManager.stop();
          engineManager = undefined;
          throw err;
        }
      }
      attestClient = engineManager.client;
      if (attestClient === undefined) {
        throw new Error("Engine started without its id-routed client.");
      }

      globalThis.__attest_engine__ = engineManager;
      globalThis.__attest_client__ = attestClient;
    },

    async teardown() {
      if (engineManager) {
        await engineManager.stop();
        engineManager = undefined;
        attestClient = undefined;
      }

      globalThis.__attest_engine__ = undefined;
      globalThis.__attest_client__ = undefined;
      globalThis.__attest_session_failures__ = undefined;
    },
  };
}
