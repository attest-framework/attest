# World fixtures: assert on what the agent did to the system it acts on

A trace records what an agent said and which tools it called. It cannot tell whether the refund API ended up with one refund or two. A *world* is a local, stateful HTTP stand-in for that system. The agent's real HTTP client talks to it, the world keeps the records the agent created, and an `effect` assertion checks the final state.

```bash
uv add "attest-ai>=1.0.1"
npm install -D "@attest-ai/core@>=1.0.1" "@attest-ai/vitest@>=1.0.1" "vitest@^4"
```

## world.yaml

The INC-1042 payments system: orders to read, refunds with idempotency-key handling, and a first refund that commits but never answers.

```yaml
world_version: 1
base_url_env: PAYMENTS_BASE_URL
seed: 1042
resources:
  orders:
    path: /orders
    routes: [get]
    seed:
      - {id: O-1042, customer: C-77, total_cents: 4999, status: delivered}
      - {id: O-2001, customer: C-12, total_cents: 1250, status: delivered}
  refunds:
    path: /refunds
    id_field: refund_id
    id_format: "R-%04d"
    routes: [create, list, get]
    filters: [order_id]
    idempotency: {header: Idempotency-Key, field: idempotency_key}
faults:
  - {id: inc-1042, resource: refunds, route: create, fault: timeout_after_commit, on_requests: [1]}
```

| Key | Meaning |
| --- | --- |
| `world_version` | Always `1`. |
| `base_url_env` | The environment variable that carries the world's base URL to the agent. Point the agent's HTTP client at it. |
| `resources.<name>` | A JSON collection. `path` is the collection path; `routes` picks from `create` (`POST path`), `list` (`GET path`, with `filters` as exact-match query parameters) and `get` (`GET path/{id}`). The server assigns `id_field` (default `id`) from `id_format`, a printf-style format whose one verb is the 1-based creation counter. `seed` lists the initial records; they appear in `state()` like any record, so scope `unique` and `idempotent` with a filter when seed records lack the idempotency key. |
| `resources.<name>.idempotency` | `create` honors this request header; the key is stored in the record under `field`. |
| `faults` | The fault script, below. |
| `seed` | Seed for probabilistic faults. |

`world.yaml` is validated against `proto/attest/v1/world-v1.schema.json` before it is used; a malformed file exits 2 with the offending path.

### Faults and determinism

A fault fires on a resource and route, by request ordinal (`on_requests`, 1-based, counted per resource and route) or by chance (`probability`, drawn from the seeded generator):

| `fault` | Effect |
| --- | --- |
| `timeout_after_commit` | `create` only. The record is committed, then the response is held until the client disconnects (at most 30 seconds). The client sees a timeout and retries; the retry is the double refund. |
| `429`, `500` | The request answers with that status and commits nothing. |

The same `seed` and the same request sequence give the same outcomes. Fired faults are listed in the snapshot's `faults_fired` (the first 1,000; `faults_fired_count` is the exact total).

### Idempotency

When a resource declares `idempotency`, the header is authoritative: a request body that sets the key field itself is refused with 400. A `create` with a key the world has already seen and the same body commits nothing and answers `200` with `Idempotent-Replayed: true` and the first record. The same key with a different body is refused with 422. A `create` without the header commits a new record every time, which is how the double refund happens.

## Run a world

```bash
attest-engine world --file world.yaml -- npm run agent       # runs the command with PAYMENTS_BASE_URL set
attest capture --name inc-1042 --expect-fail --world world.yaml -- npm run incident
attest-engine replay --name inc-1042 --world world.yaml -- npm run agent
```

`GET <base URL>/_attest/state` returns the current world-v1 snapshot: `world_version`, `definition_sha256`, `state` (the records of each resource), `faults_fired` and `faults_fired_count`. `attest-engine world --file world.yaml --serve` is the protocol the SDK helpers use: the engine prints `{"env": {...}}`, serves until a `stop` line (or end of input) arrives on stdin, then prints `{"world": <final snapshot>}`.

## In a test

Python (pytest):

```python
from attest import expect

def test_refund_is_applied_once(attest_world):
    world = attest_world("tests/payments.world.yaml")   # sets PAYMENTS_BASE_URL until the test ends
    trace = run_refund_agent("O-1042")                  # construct HTTP clients here, after the call

    expect(trace).world(world.state()).count("refunds", equals=1, order_id="O-1042")
```

TypeScript (Vitest):

```ts
import { it } from "vitest";
import { attestExpect } from "@attest-ai/core";
import { useWorld } from "@attest-ai/vitest";

it("applies the refund once", async () => {
  const world = await useWorld("tests/payments.world.yaml"); // sets PAYMENTS_BASE_URL until the test ends
  const trace = await runRefundAgent("O-1042");               // construct HTTP clients after the call

  attestExpect(trace).world(await world.state()).count("refunds", { equals: 1, where: { order_id: "O-1042" } });
});
```

Outside a test framework use `attest.start_world(path)` (a context manager) or `startWorld(path)` from `@attest-ai/core`. A session has `env`, `base_url` / `baseUrl`, `state()` (the current snapshot; raises when the world answers anything but 200) and `stop()` (restores the environment and returns the final snapshot). Both helpers take `engine_path` / `enginePath`. Only one world runs at a time in a process, because it sets a process-wide environment variable.

### Effect assertions

`world(snapshot)` takes the snapshot from `state()` or `stop()`. Each check adds an `effect` assertion (Layer 3, deterministic and free) and returns the chain, so checks continue with the rest of the DSL.

| Python | TypeScript | Fails when |
| --- | --- | --- |
| `count(resource, /, *, equals=None, at_most=None, at_least=None, where=None, **filters)` | `count(resource, { equals?, atMost?, atLeast?, where? })` | the number of records matching `where` is out of bounds. Give `equals` or `at_most` and/or `at_least`; both SDKs raise on no bound and on `equals` with another bound. |
| `unique(resource, /, *, by, where=None, **filters)` | `unique(resource, { by, where? })` | two matching records share the same values for the field(s) in `by` (a name or a list). |
| `idempotent(resource, /, *, key="idempotency_key", where=None, **filters)` | `idempotent(resource, { key?, where? })` | a matching record has no value in `key` (the record field that stores the idempotency key), or two share one. |
| `sum(resource, field, /, *, at_most, where=None, **filters)` | `sum(resource, field, { atMost, where? })` | the sum of `field` over the matching records exceeds `at_most`: a number, or a `related(...)` bound (below). |
| `matches(resource, field, related, /, *, where=None, **filters)` | `matches(resource, field, related, { where? })` | a matching record's `field` differs from the same field of its related record (below). |

`where` is exact-match filters on record fields. In Python pass them as keywords (`order_id="O-1042"`) or as a `where` dict; use the dict for fields named like a parameter (`key`, `by`, `equals`), and a key given both ways raises `TypeError`. The INC-1042 double refund fails `count` with exactly:

```text
effect violated: 2 refunds for O-1042 (expected 1)
```

A filter on several fields renders as `for amount_cents=4999, order_id=O-1042`, and a count of one drops the plural. The same agent sending the same `Idempotency-Key` on both POSTs leaves one record and passes. `world(None)` / `world(null)` (a test that ran without a world helper) is `not_evaluated` with reason `missing_evidence`. The wire format is in the "Layer 3 — Effect Checks" section of `proto/attest/v1/protocol-spec.md`.

### Relational effects: totals and recipients

A business invariant often compares two resources: the refunds against an order stay within its amount, a notification goes to the order's customer. `related(resource, field, on=key)` names the record of another resource that holds the same `key` value as the checked record, and the field to compare with. `related(...)` is the only non-literal value the contract grammar accepts, and only as the `at_most` value of `sum` or as the third argument of `matches`.

Python:

```python
from attest import expect, related

def test_inc_1077_refunds_stay_within_the_order(attest, attest_world):
    world = attest_world("tests/payments.world.yaml")
    trace = run_refund_agent("O-2077")
    snapshot = world.state()

    attest.evaluate(
        expect(trace)
        .world(snapshot)
        .sum("refunds", "amount_cents", at_most=related("orders", "amount_cents", on="order_id"))
        .world(snapshot)
        .matches("notifications", "customer_id", related("orders", "customer_id", on="order_id"))
    )
```

TypeScript:

```ts
import { attestExpect, related } from "@attest-ai/core";

attestExpect(trace)
  .world(snapshot)
  .sum("refunds", "amount_cents", { atMost: related("orders", "amount_cents", { on: "order_id" }) })
  .world(snapshot)
  .matches("notifications", "customer_id", related("orders", "customer_id", { on: "order_id" }));
```

In INC-1077 the agent refunded $12.00 and then $126.00 against order `O-2077`, whose amount is $126.00, and notified customer `C-0520` instead of the order's customer `C-0311`. Both checks fail with:

```text
effect violated: refunds amount_cents sum 13800 for O-2077 exceeds orders.amount_cents 12600
effect violated: notifications customer_id C-0520 for O-2077 does not match orders.customer_id C-0311
```

Refunding $12.00 and $114.00 passes both (the sum is 12600, within the bound). Each `world(...)` call starts the next check, so repeat it between effect checks.

| Bound | `sum` compares |
| --- | --- |
| a number (`at_most=15000`) | the total over every record matching the filters, once. The message is `effect violated: refunds amount_cents sum 13800 exceeds 12000`. |
| `related(resource, field, on=key)` | the sum of each `key` value's records against the related record's `field`, one comparison per key. The first violating key (in sorted order) is reported and the rest add `; N more violations`. |

`matches` compares every matching record's `field` with the related record's `field` for the same `key` value.

Evidence that cannot be compared is a failure, never a pass:

| Situation | Result |
| --- | --- |
| A record's `key` value has no record in the related resource | `effect violated: refunds has order_id O-9999 but orders has no record with that order_id` |
| A record has no `key` field | `effect violated: refunds record 3 has no order_id to relate to orders` |
| Two related records share the `key` value | `effect evidence: orders has more than one record with order_id O-2077, so order_id is not a unique key` |
| A summed value, or the bound, is not a number (a string, `null`, a missing field) | `effect evidence: refunds amount_cents is not a number in record 1 (null); a sum needs numeric values` |
| A number written with a decimal exponent outside ±400 | `effect evidence: refunds amount_cents in record 1: number 1e-401 has a decimal exponent outside ±400` |

Sums are exact decimals, so cents never drift. `where` filters (`order_id="O-2077"`) narrow the checked records in both checks; the related resource is not filtered.

## Compose with replay

A replay serves recorded model responses; a world serves the system the agent acts on. Use both in one test, with the model calls recorded and the side effects live:

```python
def test_inc_1042_replay(attest_replay, attest_world):
    world = attest_world("tests/payments.world.yaml")
    with attest_replay("inc-1042"):
        trace = run_refund_agent("O-1042")
    expect(trace).world(world.state()).count("refunds", equals=1, order_id="O-1042")
```

```ts
const world = await useWorld("tests/payments.world.yaml");
await useReplay("inc-1042");
```

They run as two engine processes and their environment variables merge: `OPENAI_BASE_URL` and the others from the replay, `PAYMENTS_BASE_URL` from the world.

## Capture a world

`attest.record("inc-1042", world="tests/payments.world.yaml")` and `record("inc-1042", fn, { world })` pass `--world` to `attest capture --serve`, set the world's base URL variable for the block, and save the world's final state in the fixture's `world` section. See [capture](capture.md#the-fixture).

The section is redacted like the rest of the fixture, and email addresses in record keys and values become `{{email_N}}` with the same numbering as the trace: one address is one token across the fixture, and no clear-text address is stored. Ids and timestamps in records are kept as the world held them. The section is evidence only. `replay --world` (and `attest_world` / `useWorld`) start a fresh world from `world.yaml`, never from the fixture, so templating does not affect a replay; effect checks run against the live world's `state()`, which holds the real values. A check that filters on an email address matches the live state, not a stored snapshot.

## Limits

- HTTP and JSON only: collections with create, list and get. No updates, deletes, nested resources, auth, streaming or arbitrary handlers. A world is not a general service-virtualization platform; for anything it cannot express, write a real fake.
- A world replaces an external HTTP system. In-process `mock_tools` still stand in for tool functions and are not replaced.
- At most 32 resources, 10,000 records, 1 MiB of state (fired faults included), 1,000 listed fired faults, 1 MiB per request body and 256 KiB of `world.yaml`. Numbers must be finite and integers within ±(2^53−1); quote larger ones as strings, and write no decimal exponent outside ±400.
- Every effect check embeds the snapshot in the request the engine reads, and a request line is limited to 10 MiB. `expect(...).world(snapshot)` measures the snapshot once and raises before sending when the checks of one chain would add up to more than 9 MiB (`MAX_WORLD_BYTES`); use fewer checks per test or keep the world small.
- The world listens on `127.0.0.1` under a random path token and serves nothing else.

See also: [capture](capture.md), [replay](replay.md), [behavior contracts](behavior-contracts.md).
