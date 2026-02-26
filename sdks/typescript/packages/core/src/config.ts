let _simulationMode = false;

/**
 * Configure global SDK options.
 *
 * @param options.simulation - Enable simulation mode. All `evaluateBatch` calls
 *   return deterministic pass results without spawning the engine or calling any
 *   LLM. Equivalent to setting `ATTEST_SIMULATION=1` in the environment.
 */
export function config(options: { simulation?: boolean }): void {
  if (options.simulation !== undefined) {
    _simulationMode = options.simulation;
  }
}

/**
 * Returns true when simulation mode is active via either the programmatic API
 * or the `ATTEST_SIMULATION` environment variable.
 */
export function isSimulationMode(): boolean {
  return (
    _simulationMode ||
    process.env["ATTEST_SIMULATION"] === "1" ||
    process.env["ATTEST_SIMULATION"] === "true" ||
    process.env["ATTEST_SIMULATION"] === "yes"
  );
}

/**
 * Reset all programmatic config to defaults. Primarily for use in tests.
 * Environment variables are not affected.
 */
export function resetConfig(): void {
  _simulationMode = false;
}
