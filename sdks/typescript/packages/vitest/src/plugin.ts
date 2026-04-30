import {
  EngineManager,
  AttestClient,
  isSimulationMode,
  type AgentResult,
} from "@attest-ai/core";

let engineManager: EngineManager | undefined;
let attestClient: AttestClient | undefined;

export interface AttestGlobalSetupOptions {
  enginePath?: string;
  logLevel?: string;
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
  var __attest_simulation_mode__: boolean;
  // eslint-disable-next-line no-var
  var __attest_session_failures__: AgentResult[] | undefined;
}

export function attestGlobalSetup(options?: AttestGlobalSetupOptions) {
  return {
    async setup() {
      globalThis.__attest_session_cost__ = 0;
      globalThis.__attest_session_soft_failures__ = 0;
      globalThis.__attest_session_failures__ = [];

      if (isSimulationMode()) {
        globalThis.__attest_simulation_mode__ = true;
        return;
      }

      engineManager = new EngineManager({
        enginePath: options?.enginePath,
        logLevel: options?.logLevel ?? "warn",
      });

      await engineManager.start();
      attestClient = new AttestClient(engineManager);

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
      globalThis.__attest_simulation_mode__ = false;
      globalThis.__attest_session_failures__ = undefined;
    },
  };
}
