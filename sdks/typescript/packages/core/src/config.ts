let _simulationMode = false;
let _sampleRate = 0;

/**
 * Configure global SDK options.
 *
 * @param options.simulation - Enable simulation mode. All `evaluateBatch` calls
 *   return deterministic pass results without spawning the engine or calling any
 *   LLM. Equivalent to setting `ATTEST_SIMULATION=1` in the environment.
 * @param options.sampleRate - Sampling rate for continuous evaluation (0.0–1.0).
 *   Equivalent to setting `ATTEST_SAMPLE_RATE` in the environment.
 */
export function config(options: {
  simulation?: boolean;
  sampleRate?: number;
}): void {
  if (options.simulation !== undefined) {
    _simulationMode = options.simulation;
  }
  if (options.sampleRate !== undefined) {
    if (options.sampleRate < 0 || options.sampleRate > 1) {
      throw new RangeError(
        `sampleRate must be between 0.0 and 1.0, got ${options.sampleRate}`,
      );
    }
    _sampleRate = options.sampleRate;
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
 * Returns the sampling rate for continuous evaluation. Priority:
 * programmatic `config({ sampleRate })` > `ATTEST_SAMPLE_RATE` env var > 0.
 */
export function getSampleRate(): number {
  if (_sampleRate > 0) return _sampleRate;
  const env = process.env["ATTEST_SAMPLE_RATE"];
  if (env !== undefined && env !== "") {
    const parsed = Number(env);
    if (!Number.isFinite(parsed) || parsed < 0 || parsed > 1) {
      throw new RangeError(
        `ATTEST_SAMPLE_RATE must be between 0.0 and 1.0, got '${env}'`,
      );
    }
    return parsed;
  }
  return 0;
}

/**
 * Reset all programmatic config to defaults. Primarily for use in tests.
 * Environment variables are not affected.
 */
export function resetConfig(): void {
  _simulationMode = false;
  _sampleRate = 0;
}
