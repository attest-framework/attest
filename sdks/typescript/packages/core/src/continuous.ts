import { EventEmitter } from "node:events";
import type { Assertion, EvaluateBatchResult, Trace } from "./proto/types.js";
import type { AttestClient } from "./client.js";

const DEFAULT_QUEUE_SIZE = 1000;

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
  drift_type: string;
  score: number | string;
  trace_id: string;
  [key: string]: unknown;
}

export class AlertDispatcher extends EventEmitter {
  private readonly webhookUrl: string | undefined;
  private readonly slackUrl: string | undefined;

  constructor(webhookUrl?: string, slackUrl?: string) {
    super();
    this.webhookUrl = webhookUrl;
    this.slackUrl = slackUrl;
  }

  async dispatch(alert: AlertPayload): Promise<void> {
    this.emit("alert", alert);

    const tasks: Promise<void>[] = [];

    if (this.webhookUrl) {
      tasks.push(this.postJson(this.webhookUrl, alert));
    }
    if (this.slackUrl) {
      const text = `[attest] drift alert -- type=${alert.drift_type} score=${alert.score} trace_id=${alert.trace_id}`;
      tasks.push(this.postJson(this.slackUrl, { text }));
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
      signal: AbortSignal.timeout(10_000),
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
  private running = false;
  private loopTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    client: AttestClient,
    assertions: readonly Assertion[],
    options?: {
      sampleRate?: number;
      alertWebhook?: string;
      alertSlackUrl?: string;
      maxSize?: number;
    },
  ) {
    this.client = client;
    this.assertions = assertions;
    this.sampler = new Sampler(options?.sampleRate ?? 1.0);
    this.dispatcher = new AlertDispatcher(options?.alertWebhook, options?.alertSlackUrl);
    this.maxSize = options?.maxSize ?? resolveQueueSize();
  }

  get alertEmitter(): AlertDispatcher {
    return this.dispatcher;
  }

  async evaluateTrace(trace: Trace): Promise<EvaluateBatchResult | null> {
    if (!this.sampler.shouldSample()) return null;
    return this.client.evaluateBatch(trace, this.assertions);
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

  stop(): void {
    this.running = false;
    if (this.loopTimer !== null) {
      clearTimeout(this.loopTimer);
      this.loopTimer = null;
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
        } catch {
          // logged via alertEmitter events if needed
        }
      }

      this.scheduleNext();
    }, this.queue.length > 0 ? 0 : 100);
  }
}
