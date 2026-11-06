import { EventEmitter } from "node:events";
import type { Assertion, AssertionResult, DriftReport, EvaluateBatchResult, Trace } from "./proto/types.js";
import type { AttestClient } from "./client.js";
import { AssertionIdAssigner } from "./assertion-id.js";

const DEFAULT_QUEUE_SIZE = 1000;
const DEFAULT_DISPATCH_TIMEOUT_MS = 10_000;
const DEFAULT_DRAIN_TIMEOUT_MS = 15_000;
/** Static threshold used for dynamic specs and specs without a numeric threshold (C6). */
const DEFAULT_STATIC_THRESHOLD = 0.8;
const STATUS_HARD_FAIL = "hard_fail";
const STATUS_DRIFT_DETECTED = "drift_detected";

function resolveQueueSize(): number {
  const raw = process.env["ATTEST_CONTINUOUS_QUEUE_SIZE"];
  if (raw !== undefined && raw !== "") {
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed > 0) return parsed;
  }
  return DEFAULT_QUEUE_SIZE;
}

export class Sampler {
  private readonly rate: number;

  constructor(rate: number) {
    if (rate < 0 || rate > 1) {
      throw new Error(`sample_rate must be in [0.0, 1.0], got ${rate}`);
    }
    this.rate = rate;
  }

  shouldSample(): boolean {
    return Math.random() < this.rate;
  }
}

export type AlertStatus = "drift_detected" | "hard_fail";

/** Webhook JSON body shared by both SDK runners. Every key is always present. */
export interface AlertPayload {
  runner: string;
  assertion_id: string;
  /** Alert trigger: `hard_fail` wins over `drift_detected` when both hold. */
  status: AlertStatus;
  /** Status of the evaluated assertion result. */
  result_status: "pass" | "soft_fail" | "hard_fail";
  score: number | null;
  baseline_mean: number | null;
  baseline_count: number | null;
  static_threshold: number;
  trace_id: string | null;
}

function slackText(alert: AlertPayload): string {
  return (
    `[attest] drift alert \u2014 runner=${alert.runner} assertion=${alert.assertion_id} ` +
    `status=${alert.status} score=${alert.score} baseline_mean=${alert.baseline_mean} ` +
    `baseline_count=${alert.baseline_count} static_threshold=${alert.static_threshold} ` +
    `trace_id=${alert.trace_id}`
  );
}


export class AlertDispatcher extends EventEmitter {
  private readonly webhookUrl: string | undefined;
  private readonly slackUrl: string | undefined;

  private readonly timeoutMs: number;

  constructor(webhookUrl?: string, slackUrl?: string, timeoutMs: number = DEFAULT_DISPATCH_TIMEOUT_MS) {
    super();
    this.webhookUrl = webhookUrl;
    this.slackUrl = slackUrl;
    this.timeoutMs = timeoutMs;
  }

  async dispatch(alert: AlertPayload): Promise<void> {
    this.emit("alert", alert);

    const tasks: Promise<void>[] = [];

    if (this.webhookUrl) {
      tasks.push(this.postJson(this.webhookUrl, alert));
    }
    if (this.slackUrl) {
      tasks.push(this.postJson(this.slackUrl, { text: slackText(alert) }));
    }

    const results = await Promise.allSettled(tasks);
    for (const result of results) {
      if (result.status === "rejected") {
        this.emit("alertError", result.reason);
      }
    }
  }

  private async postJson(url: string, payload: object): Promise<void> {
    const response = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: AbortSignal.timeout(this.timeoutMs),
    });
    if (!response.ok) {
      throw new Error(`POST to ${url} returned ${response.status}`);
    }
  }
}

export class ContinuousEvalRunner {
  private readonly client: AttestClient;
  private readonly assertions: readonly Assertion[];
  private readonly sampler: Sampler;
  private readonly dispatcher: AlertDispatcher;
  private readonly queue: Trace[] = [];
  private readonly maxSize: number;
  private readonly name: string;
  private readonly drainTimeoutMs: number;
  private readonly onError: ((error: unknown) => void) | undefined;
  private readonly inFlight = new Set<Promise<unknown>>();
  private chain: Promise<unknown> = Promise.resolve();
  private running = false;
  private loopTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    client: AttestClient,
    assertions: readonly Assertion[],
    options: {
      /** Id scope of the assertions (C3): their ids derive from this name, so keep it stable. */
      name: string;
      sampleRate?: number;
      alertWebhook?: string;
      alertSlackUrl?: string;
      maxSize?: number;
      /** Called with every evaluation, drift-read and dispatch error (also logged and emitted as `alertError`). */
      onError?: (error: unknown) => void;
      /** Bound on one alert POST (default 10000). */
      dispatchTimeoutMs?: number;
      /** Bound on how long `stop()` waits for in-flight work (default 15000). */
      drainTimeoutMs?: number;
    },
  ) {
    this.client = client;
    if (typeof options?.name !== "string" || options.name === "") {
      throw new Error("ContinuousEvalRunner requires options.name: it is the scope of the assertion ids");
    }
    const ids = new AssertionIdAssigner(options.name);
    this.assertions = assertions.map((a) => ({
      ...a,
      assertion_id: ids.next(a.type, a.spec),
      scope_id: options.name,
    }));
    this.sampler = new Sampler(options.sampleRate ?? 1.0);
    this.name = options.name;
    this.onError = options.onError;
    this.drainTimeoutMs = options.drainTimeoutMs ?? DEFAULT_DRAIN_TIMEOUT_MS;
    this.dispatcher = new AlertDispatcher(
      options.alertWebhook,
      options.alertSlackUrl,
      options.dispatchTimeoutMs ?? DEFAULT_DISPATCH_TIMEOUT_MS,
    );
    this.dispatcher.on("alertError", (error: unknown) => this.reportError(error));
    this.maxSize = options.maxSize ?? resolveQueueSize();
  }

  get alertEmitter(): AlertDispatcher {
    return this.dispatcher;
  }

  /** Runs `fn` after every previously queued evaluation, so evaluate → query_drift pairs never interleave. */
  private serialize<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.chain.then(fn, fn);
    this.chain = run.catch(() => undefined);
    return run;
  }

  /** Surfaces an error to the log and `onError` without ever throwing or leaving a rejected promise. */
  private reportError(error: unknown): void {
    try {
      console.error(`attest continuous-eval runner ${JSON.stringify(this.name)}:`, error);
    } catch {
      // logging must not break the loop; nothing further to surface through
    }
    if (this.onError === undefined) return;
    try {
      const returned: unknown = this.onError(error);
      if (returned instanceof Promise) {
        returned.catch((observerError: unknown) => this.logObserverFailure(observerError));
      }
    } catch (observerError) {
      this.logObserverFailure(observerError);
    }
  }

  private logObserverFailure(observerError: unknown): void {
    try {
      console.error(`attest continuous-eval runner ${JSON.stringify(this.name)}: onError callback failed:`, observerError);
    } catch {
      // see reportError
    }
  }

  private emitError(error: unknown): void {
    try {
      this.dispatcher.emit("alertError", error);
    } catch (listenerError) {
      this.logObserverFailure(listenerError);
    }
  }

  async evaluateTrace(trace: Trace): Promise<EvaluateBatchResult | null> {
    if (!this.sampler.shouldSample()) return null;
    const work = this.serialize(() => this.evaluateAndAlert(trace));
    this.inFlight.add(work);
    try {
      return await work;
    } finally {
      this.inFlight.delete(work);
    }
  }

  private async evaluateAndAlert(trace: Trace): Promise<EvaluateBatchResult> {
    const result = await this.client.evaluateBatch(trace, this.assertions);
    // The engine records each score before it answers, and query_drift judges the
    // assertion's newest history row, so this loop runs inside the runner's lock.
    for (const r of result.results) {
      await this.alertIfNeeded(trace, r);
    }
    return result;
  }

  private staticThresholdFor(assertionId: string): number {
    const spec = this.assertions.find((a) => a.assertion_id === assertionId)?.spec as
      | Record<string, unknown>
      | undefined;
    const threshold = spec?.["threshold"];
    return typeof threshold === "number" && threshold > 0 ? threshold : DEFAULT_STATIC_THRESHOLD;
  }

  private async alertIfNeeded(trace: Trace, result: AssertionResult): Promise<void> {
    if (result.status === "not_evaluated") return;
    const staticThreshold = this.staticThresholdFor(result.assertion_id);
    let report: DriftReport | undefined;
    try {
      report = await this.client.queryDrift(result.assertion_id, undefined, { staticThreshold });
    } catch (error) {
      this.emitError(error);
    }
    const hardFailed = result.status === STATUS_HARD_FAIL;
    const drifted = report?.status === STATUS_DRIFT_DETECTED;
    if (!drifted && !hardFailed) return;
    const alert: AlertPayload = {
      runner: this.name,
      assertion_id: result.assertion_id,
      status: hardFailed ? STATUS_HARD_FAIL : STATUS_DRIFT_DETECTED,
      result_status: result.status as AlertPayload["result_status"],
      score: typeof result.score === "number" ? result.score : null,
      baseline_mean: report?.mean ?? null,
      baseline_count: report?.count ?? null,
      static_threshold: staticThreshold,
      trace_id: trace.trace_id ?? null,
    };
    try {
      await this.dispatcher.dispatch(alert);
    } catch (error) {
      this.emitError(error);
    }
  }

  submit(trace: Trace): boolean {
    if (this.queue.length >= this.maxSize) {
      return false;
    }
    this.queue.push(trace);
    return true;
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.scheduleNext();
  }

  /**
   * Stops the loop, then processes the traces already queued and waits for in-flight evaluations
   * and dispatches, all bounded by `drainTimeoutMs`. At the bound the whole remaining queue is
   * discarded (nothing is evaluated after the timeout and nothing is left for a restart), the
   * evaluation already in flight is left to finish, and the timeout with the number of discarded
   * traces is reported through `onError`.
   */
  async stop(): Promise<void> {
    this.running = false;
    if (this.loopTimer !== null) {
      clearTimeout(this.loopTimer);
      this.loopTimer = null;
    }
    if (this.inFlight.size === 0 && this.queue.length === 0) return;
    let expired = false;
    let discarded = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const bound = new Promise<"timeout">((resolve) => {
      timer = setTimeout(() => {
        expired = true;
        discarded = this.queue.splice(0).length;
        resolve("timeout");
      }, this.drainTimeoutMs);
    });
    const drained = this.drainQueue(() => expired).then(() => "drained" as const);
    const outcome = await Promise.race([drained, bound]);
    clearTimeout(timer);
    if (outcome === "timeout") {
      this.emitError(
        new Error(
          `stop() gave up after ${this.drainTimeoutMs} ms with ${this.inFlight.size} evaluation(s) in flight; discarded ${discarded} queued trace(s) not evaluated`,
        ),
      );
    }
  }

  private async drainQueue(expired: () => boolean): Promise<void> {
    while (!expired()) {
      const trace = this.queue.shift();
      if (trace === undefined) break;
      try {
        await this.evaluateTrace(trace);
      } catch (error) {
        this.reportError(error);
      }
    }
    await Promise.allSettled([...this.inFlight]);
  }

  get queueLength(): number {
    return this.queue.length;
  }

  get isRunning(): boolean {
    return this.running;
  }

  private scheduleNext(): void {
    if (!this.running) return;

    this.loopTimer = setTimeout(() => {
      void this.tick();
    }, this.queue.length > 0 ? 0 : 100);
  }

  private async tick(): Promise<void> {
    try {
      if (!this.running) return;
      const trace = this.queue.shift();
      if (trace !== undefined) {
        try {
          await this.evaluateTrace(trace);
        } catch (error) {
          this.reportError(error);
        }
      }
    } finally {
      this.scheduleNext();
    }
  }
}
