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

export interface AlertPayload {
  /** Why the alert fired: `drift_detected` or `hard_fail`. */
  drift_type: string;
  score: number | string;
  trace_id: string;
  /** Runner name (the assertion id scope). */
  runner?: string;
  assertion_id?: string;
  /** Status of the evaluated assertion result. */
  status?: string;
  baseline_mean?: number;
  baseline_count?: number;
  static_threshold?: number;
  [key: string]: unknown;
}

function slackText(alert: AlertPayload): string {
  const parts = [`[attest] drift alert -- type=${alert.drift_type}`];
  if (alert.runner !== undefined) parts.push(`runner=${alert.runner}`);
  if (alert.assertion_id !== undefined) parts.push(`assertion_id=${alert.assertion_id}`);
  if (alert.status !== undefined) parts.push(`status=${alert.status}`);
  parts.push(`score=${alert.score}`);
  if (alert.baseline_mean !== undefined) parts.push(`baseline_mean=${alert.baseline_mean}`);
  if (alert.baseline_count !== undefined) parts.push(`baseline_count=${alert.baseline_count}`);
  if (alert.static_threshold !== undefined) parts.push(`static_threshold=${alert.static_threshold}`);
  parts.push(`trace_id=${alert.trace_id}`);
  return parts.join(" ");
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

  private async postJson(url: string, payload: Record<string, unknown>): Promise<void> {
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
    this.dispatcher.on("alertError", (error: unknown) => {
      console.error(`attest continuous-eval runner ${JSON.stringify(this.name)}:`, error);
      this.onError?.(error);
    });
    this.maxSize = options.maxSize ?? resolveQueueSize();
  }

  get alertEmitter(): AlertDispatcher {
    return this.dispatcher;
  }

  async evaluateTrace(trace: Trace): Promise<EvaluateBatchResult | null> {
    if (!this.sampler.shouldSample()) return null;
    const work = this.evaluateAndAlert(trace);
    this.inFlight.add(work);
    try {
      return await work;
    } finally {
      this.inFlight.delete(work);
    }
  }

  private async evaluateAndAlert(trace: Trace): Promise<EvaluateBatchResult> {
    const result = await this.client.evaluateBatch(trace, this.assertions);
    // The engine records each score before it answers, so the drift read below
    // judges this run's row.
    await Promise.all(result.results.map((r) => this.alertIfNeeded(trace, r)));
    return result;
  }

  private staticThresholdFor(assertionId: string): number {
    const spec = this.assertions.find((a) => a.assertion_id === assertionId)?.spec as
      | Record<string, unknown>
      | undefined;
    const threshold = spec?.["threshold"];
    return typeof threshold === "number" ? threshold : DEFAULT_STATIC_THRESHOLD;
  }

  private async alertIfNeeded(trace: Trace, result: AssertionResult): Promise<void> {
    if (result.status === "not_evaluated") return;
    const staticThreshold = this.staticThresholdFor(result.assertion_id);
    let report: DriftReport | undefined;
    try {
      report = await this.client.queryDrift(result.assertion_id, undefined, { staticThreshold });
    } catch (error) {
      this.dispatcher.emit("alertError", error);
    }
    const drifted = report?.status === STATUS_DRIFT_DETECTED;
    const hardFailed = result.status === STATUS_HARD_FAIL;
    if (!drifted && !hardFailed) return;
    const alert: AlertPayload = {
      drift_type: drifted ? STATUS_DRIFT_DETECTED : STATUS_HARD_FAIL,
      runner: this.name,
      assertion_id: result.assertion_id,
      status: result.status,
      score: result.score,
      trace_id: trace.trace_id,
      static_threshold: staticThreshold,
    };
    if (report !== undefined) {
      alert.baseline_mean = report.mean;
      alert.baseline_count = report.count;
    }
    try {
      await this.dispatcher.dispatch(alert);
    } catch (error) {
      this.dispatcher.emit("alertError", error);
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

  /** Stops the loop, then waits (bounded by `drainTimeoutMs`) for in-flight evaluations and dispatches. */
  async stop(): Promise<void> {
    this.running = false;
    if (this.loopTimer !== null) {
      clearTimeout(this.loopTimer);
      this.loopTimer = null;
    }
    if (this.inFlight.size === 0) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const bound = new Promise<"timeout">((resolve) => {
      timer = setTimeout(() => resolve("timeout"), this.drainTimeoutMs);
    });
    const drained = Promise.allSettled([...this.inFlight]).then(() => "drained" as const);
    const outcome = await Promise.race([drained, bound]);
    clearTimeout(timer);
    if (outcome === "timeout") {
      this.dispatcher.emit(
        "alertError",
        new Error(`stop() gave up after ${this.drainTimeoutMs} ms with ${this.inFlight.size} evaluation(s) in flight`),
      );
    }
  }

  get queueLength(): number {
    return this.queue.length;
  }

  get isRunning(): boolean {
    return this.running;
  }

  private scheduleNext(): void {
    if (!this.running) return;

    this.loopTimer = setTimeout(async () => {
      if (!this.running) return;

      const trace = this.queue.shift();
      if (trace !== undefined) {
        try {
          await this.evaluateTrace(trace);
        } catch (error) {
          this.dispatcher.emit("alertError", error);
        }
      }

      this.scheduleNext();
    }, this.queue.length > 0 ? 0 : 100);
  }
}
