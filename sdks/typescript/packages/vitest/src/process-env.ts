/**
 * Names and checks shared by the main process (the reporter and the global setup) and the
 * workers. This module must not import `vitest`: the main process loads it outside a worker,
 * where Vitest 3 throws on a runtime import.
 */

/**
 * Set by AttestReporter in the main process before workers start: the Vitest
 * root its report paths are relative to, and the directory where workers drop
 * the usage of evaluations that belong to no test.
 */
export const ROOT_ENV = "ATTEST_VITEST_ROOT";
export const USAGE_DIR_ENV = "ATTEST_VITEST_USAGE_DIR";

/** File extension of the markers a worker writes for a budget-exceeded evaluation outside a test. */
export const BUDGET_MARKER_EXT = ".budget-exceeded";

/** How attestGlobalSetup({ minPassRate }) reaches the workers that run trials(). */
export const MIN_PASS_RATE_ENV = "ATTEST_MIN_PASS_RATE";

/** A pass rate in [0, 1]; anything else is an error naming where it came from. */
export function validateMinPassRate(rate: number, source: string): number {
  if (!Number.isFinite(rate) || rate < 0 || rate > 1) {
    throw new Error(`minPassRate must be a number between 0 and 1, got ${source}`);
  }
  return rate;
}
