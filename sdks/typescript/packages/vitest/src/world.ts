/**
 * `useWorld(path)`: host a world for the running test, a stateful HTTP stand-in for the
 * system the agent acts on, and stop it when the test ends.
 */
import { onTestFinished } from "vitest";
import { startWorld, type WorldOptions, type WorldSession } from "@attest-ai/core";

/**
 * Start the world defined by `worldFile` (a `world.yaml`) for the running test and set its
 * base URL variable (`PAYMENTS_BASE_URL` for a `payments` world). Call it before
 * constructing the HTTP client (clients read the variable at construction). Read effects
 * with `await world.state()` and assert on them with
 * `expect(trace).world(await world.state()).count(...)`. Composes with `useReplay` in the
 * same test.
 */
export async function useWorld(worldFile: string, opts: WorldOptions = {}): Promise<WorldSession> {
  const session = await startWorld(worldFile, opts);
  onTestFinished(async () => {
    await session.stop();
  });
  return session;
}
