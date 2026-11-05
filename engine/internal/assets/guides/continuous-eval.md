# Continuous eval and drift alerts

`ContinuousEvalRunner` evaluates live traces against a fixed set of assertions in the background and posts an alert to a webhook or Slack when an assertion hard-fails or its score drops below its own history. The runner is in `attest-ai` (Python) and `@attest-ai/core` (TypeScript). Both send the same alert.

Drift is measured against the engine's history of that assertion, so the assertions must be stable: the history is keyed by the assertion id, and the id comes from the runner `name` (see [Name and ids](#name-and-ids)).

## How an alert is decided

For each sampled trace the runner calls `evaluate_batch`, then asks the engine `query_drift` for every row that was evaluated, and alerts when either holds:

| Trigger | `status` in the alert |
|---|---|
| The row is `hard_fail` | `hard_fail` |
| `query_drift` reports `drift_detected` | `drift_detected` |

When both hold, `status` is `hard_fail`. A `not_evaluated` row is neither queried nor alerted. A `pass` or `soft_fail` row alerts only on drift.

`query_drift` judges the assertion's newest history row against a baseline of the 50 passing and soft-failing runs before it. Hard failures never enter the baseline, so a run of failures cannot become the bar. The row drifted when its score is below `max(static_threshold, mean − 2·max(σ, 0.02))`, where `static_threshold` is the assertion's own threshold (a numeric `threshold` above 0, otherwise `0.8`, which is also what `"dynamic"` uses). The window of 50, the factor 2 and the σ floor of 0.02 are fixed: nothing in the runner, the SDKs or the environment changes them, and the `window_size` field of `query_drift` is accepted and ignored. See [Dynamic thresholds](judges.md#dynamic-thresholds).

The engine records a score before it answers `evaluate_batch`, and `query_drift` reads the assertion's latest history row, not the row of a given trace. Each runner therefore runs evaluate and drift read as one step, one trace at a time: direct calls and the background loop share one lock (Python) or one queue (TypeScript), so two traces never interleave.

### Cold start

A baseline is active only with at least 10 passing or soft-failing runs. Before that, `query_drift` reports `ok`, no `drift_detected` alert is sent, and a `hard_fail` alert still goes out, judged by the static threshold alone. The same rule applies after an [upgrade](#upgrading): ids changed, so every assertion starts with an empty baseline.

## Set up the runner

Build the assertions with the same DSL as a test, then hand them to the runner with an engine client. `expect()` is only a builder here: the assertions are evaluated against the traces you submit, not against the trace passed to `expect()`.

Python:

```python
import asyncio

from attest import ContinuousEvalRunner, TraceBuilder, expect
from attest.client import AttestClient
from attest.engine_manager import EngineManager


def production_trace(n: int):
    return TraceBuilder("support-agent").set_output(message=f"Refund issued, reference {n}.").build()


def log_error(exc: Exception) -> None:
    print("on_error:", type(exc).__name__, exc)


async def main() -> None:
    assertions = (
        expect(production_trace(0))
        .passes_judge("Does the reply tell the customer the refund was issued?", threshold=0.8)
        .assertions
    )
    async with EngineManager() as engine:
        runner = ContinuousEvalRunner(
            AttestClient(engine),
            assertions,
            sample_rate=1.0,
            name="support-agent-prod",
            alert_webhook="https://alerts.example.com/attest",
            alert_slack_url="https://hooks.slack.com/services/T000/B000/XXXX",
            on_error=log_error,
        )
        await runner.start()
        await runner.submit(production_trace(1))  # queued; the loop evaluates it
        await runner.stop()  # drains the queue, then stops


asyncio.run(main())
```

TypeScript:

```typescript
import { AttestClient, ContinuousEvalRunner, EngineManager, TraceBuilder, attestExpect } from "@attest-ai/core";

function productionTrace(n: number) {
  return new TraceBuilder("support-agent").setOutput({ message: `Refund issued, reference ${n}.` }).build();
}

const assertions = attestExpect(productionTrace(0)).passesJudge(
  "Does the reply tell the customer the refund was issued?",
  { threshold: 0.8 },
).assertions;

const engine = new EngineManager();
await engine.start();
const runner = new ContinuousEvalRunner(new AttestClient(engine), assertions, {
  name: "support-agent-prod",
  sampleRate: 1.0,
  alertWebhook: "https://alerts.example.com/attest",
  alertSlackUrl: "https://hooks.slack.com/services/T000/B000/XXXX",
  onError: (error) => console.error("onError:", error),
});
runner.start();
runner.submit(productionTrace(1)); // queued; the loop evaluates it
await runner.stop(); // drains queued traces, waits for the evaluation in flight, then stops
await engine.stop();
```

Judge and embedding assertions need a configured provider like any other run, see [judges](judges.md) and [configuration](configuration.md). A row without a provider is `not_evaluated` and is not alerted.

### Options

| | Python (`ContinuousEvalRunner(client, assertions, ...)`) | TypeScript (`new ContinuousEvalRunner(client, assertions, options)`) |
|---|---|---|
| Scope of the assertion ids, required | `name=` (keyword only) | `name` |
| Fraction of traces evaluated, default 1.0, must be in [0, 1] | `sample_rate` | `sampleRate` |
| Webhook (JSON body) | `alert_webhook` | `alertWebhook` |
| Slack incoming webhook | `alert_slack_url` | `alertSlackUrl` |
| Queue bound | `maxsize`, else `ATTEST_CONTINUOUS_QUEUE_SIZE`, else 1000 | `maxSize`, else `ATTEST_CONTINUOUS_QUEUE_SIZE`, else 1000 |
| Error callback | `on_error` | `onError` |
| Bound on one alert POST, default 10 s | `dispatch_timeout_s` | `dispatchTimeoutMs` |
| Bound on `stop()` (queue drain plus in-flight work), default 30 s (Python) / 15 s (TypeScript) | `drain_timeout_s` | `drainTimeoutMs` |

`start()` and `submit()` are coroutines in Python (`await` both) and plain calls in TypeScript. A full queue drops the trace: Python logs a warning, TypeScript's `submit()` returns `false`. To evaluate one trace right away instead of queueing it, call `evaluate_trace(trace)` (Python) or `evaluateTrace(trace)` (TypeScript). The call returns the batch result, or `None` / `null` when sampling skipped the trace.

Either destination may be omitted. When the Python runner is given no `alert_webhook` or `alert_slack_url`, it falls back to `attest.config(alert_webhook=..., alert_slack_url=...)`, then to the `ATTEST_ALERT_WEBHOOK` and `ATTEST_ALERT_SLACK_URL` environment variables. An explicit runner argument always wins. TypeScript has no global alert configuration; pass `alertWebhook` and `alertSlackUrl` to the runner.

In both SDKs `stop()` first drains the traces still queued, then waits for the evaluation in flight, and returns when both finish or when `drain_timeout_s` / `drainTimeoutMs` elapses.

Python reads drift only when a destination is set and skips the read in simulation mode, so an alert in simulation mode carries `null` baseline fields.

## Alert payload

The webhook receives one POST per alerting assertion per trace, with a JSON body. Every key is always present; a value that is unknown is `null`.

| Key | Type | Meaning |
|---|---|---|
| `runner` | string | The runner `name` |
| `assertion_id` | string | The assertion id (`a2_…`), the key of its history |
| `status` | `"drift_detected"` or `"hard_fail"` | Why the alert fired (the trigger) |
| `result_status` | `"pass"`, `"soft_fail"` or `"hard_fail"` | The status of the evaluation itself |
| `score` | number or null | The evaluation's score |
| `baseline_mean` | number or null | Mean of the baseline; null when the drift read failed or was skipped |
| `baseline_count` | integer or null | Rows in the baseline; null as above |
| `static_threshold` | number | The static bar the dynamic bar never falls below |
| `trace_id` | string or null | The evaluated trace |

A drifting pass (a static `threshold=0.8` judge that scored 1.0 ten times and then 0.85):

```json
{"runner": "support-agent-prod", "assertion_id": "a2_a823ee7cc0a7ae497d15376fea9c1f77", "status": "drift_detected", "result_status": "pass", "score": 0.85, "baseline_mean": 1, "baseline_count": 10, "static_threshold": 0.8, "trace_id": "trc_574dc167f02c"}
```

A hard fail (`threshold="dynamic"`, ten runs at 0.9 and then 0.5):

```json
{"runner": "support-agent-prod", "assertion_id": "a2_b328096a69ea78d9327faeb1d4832f97", "status": "hard_fail", "result_status": "hard_fail", "score": 0.5, "baseline_mean": 0.9000000000000001, "baseline_count": 10, "static_threshold": 0.8, "trace_id": "trc_2b1c24e0d3c8"}
```

The Slack body is `{"text": "…"}` rendered from the same fields:

```text
[attest] drift alert — runner=support-agent-prod assertion=a2_a823ee7cc0a7ae497d15376fea9c1f77 status=drift_detected score=0.85 baseline_mean=1 baseline_count=10 static_threshold=0.8 trace_id=trc_574dc167f02c
```

## Errors

The runner never lets an alert problem fail an evaluation or stop the loop. Each of these goes to the error callback and to the log, and nothing else:

- A `query_drift` call that fails. The alert still goes out for a `hard_fail` row, with `baseline_mean` and `baseline_count` null; a drifting pass is not alerted, because without the read there is nothing to alert on.
- A destination that is down, times out, or answers anything but 2xx. The other destination is still tried.
- An evaluation that throws in the background loop. The trace is lost; the loop goes on with the next one.
- A `stop()` that ran out of time (see below).

A failing error callback is logged and ignored. TypeScript also emits `alertError` on `runner.alertEmitter` for every dispatch error and `alert` for every payload before it is posted.

## Stop and drain

`stop()` ends the loop, evaluates and alerts the traces still queued, then waits for the evaluation in flight, all bounded by the drain timeout (`drain_timeout_s` / `drainTimeoutMs`). Traces submitted before `stop()` are therefore not lost in either SDK.

On timeout the runner reports an error through the callback and stops waiting. Work still pending at the bound is dropped: Python cancels it, TypeScript abandons the queued traces (the error message gives the counts).

## Name and ids

The assertion ids derive from the runner `name`, the assertion type, its canonical spec and the occurrence among identical assertions. Keep the `name` stable across restarts and distinct between runners: a new name is a new scope, so every baseline starts over. Editing a spec also gives the assertion a new id. Reordering distinct assertions does not. See [baselines](baselines.md#removed-and-changed-assertions).

## Upgrading

History is keyed by these ids. Rows written by earlier versions used ordinal ids and are not matched, so after upgrading to 1.1 each assertion rebuilds its baseline: the first 10 passing or soft-failing runs are the [cold start](#cold-start). The old rows stay in the history database; pruning, when enabled, removes them with the rest of the old history ([configuration](configuration.md#cache-and-history)).
