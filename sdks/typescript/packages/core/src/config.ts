let _simulationMode = false;
let _sampleRate = 0;

/**
 * Configure global SDK options.
 *
 * @param options.simulation - Enable simulation mode: the engine evaluates
 *   layers 1-4 and reports layers 5-6 as not evaluated (reason "simulation")
 *   without calling any provider. The engine reads `ATTEST_SIMULATION` when it
 *   starts; the Vitest fixtures set it from this flag. Equivalent to setting
 *   `ATTEST_SIMULATION=1` in the environment.
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
  return _simulationMode || simulationEnvEnabled();
}

/**
 * True when `ATTEST_SIMULATION` is set for this process (and so for an
 * engine it spawns): "1", "true" or "yes", trimmed and case-insensitive.
 */
export function simulationEnvEnabled(): boolean {
  const env = (process.env["ATTEST_SIMULATION"] ?? "").trim().toLowerCase();
  return env === "1" || env === "true" || env === "yes";
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
