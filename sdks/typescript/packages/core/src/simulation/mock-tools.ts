import { activeMockRegistry, type MockToolFn } from "./context.js";

/** Registry of mock tool implementations, activated for the duration of `run`. */
export class MockToolRegistry {
  private readonly tools = new Map<string, MockToolFn>();

  register(name: string, fn: MockToolFn): void {
    this.tools.set(name, fn);
  }

  /**
   * Run `fn` with the registered mocks active. `TraceBuilder.addToolCall`
   * returns the mock's result for a registered tool name. Scopes are
   * isolated per async call chain (AsyncLocalStorage).
   */
  run<T>(fn: () => T): T {
    return activeMockRegistry.run(new Map(this.tools), fn);
  }
}

export type MockTool<F extends MockToolFn> = F & { readonly mockToolName: string };

/** Tag `fn` as the mock for tool `name`; the returned function behaves like `fn`. */
export function mockTool<F extends MockToolFn>(name: string, fn: F): MockTool<F> {
  const wrapper = ((args: Record<string, unknown>) => fn(args)) as F;
  return Object.assign(wrapper, { mockToolName: name });
}
