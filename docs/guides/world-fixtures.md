# World fixtures: assert on what the agent did to the system it acts on

A trace records what an agent said and which tools it called. It cannot tell whether the refund API ended up with one refund or two. A *world* is a local, stateful HTTP stand-in for that system. The agent's real HTTP client talks to it, the world keeps the records the agent created, and an `effect` assertion checks the final state.

```bash
uv add "attest-ai>=1.0.0rc1"
npm install -D "@attest-ai/core@>=1.0.0-rc.1" "@attest-ai/vitest@>=1.0.0-rc.1" "vitest@^4"
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

The same `seed` and the same request sequence give the same outcomes. Fired faults are listed in the snapshot's `faults_fired`.

### Idempotency

When a resource declares `idempotency`, the header is authoritative: a request body that sets the key field itself is refused with 400. A `create` with a key the world has already seen and the same body commits nothing and answers `200` with `Idempotent-Replayed: true` and the first record. The same key with a different body is refused with 422. A `create` without the header commits a new record every time, which is how the double refund happens.

## Run a world

```bash
attest-engine world --file world.yaml -- npm run agent       # runs the command with PAYMENTS_BASE_URL set
attest capture --name inc-1042 --expect-fail --world world.yaml -- npm run incident
attest-engine replay --name inc-1042 --world world.yaml -- npm run agent
```

`GET <base URL>/_attest/state` returns the current world-v1 snapshot: `world_version`, `definition_sha256`, `state` (the records of each resource) and `faults_fired`. `attest-engine world --file world.yaml --serve` is the protocol the SDK helpers use: the engine prints `{"env": {...}}`, serves until a `stop` line (or end of input) arrives on stdin, then prints `{"world": <final snapshot>}`.

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
| `count(resource, *, equals=None, at_most=None, at_least=None, **where)` | `count(resource, { equals?, atMost?, atLeast?, where? })` | the number of records matching `where` is out of bounds. Give `equals` or `at_most` and/or `at_least`; both SDKs raise on no bound and on `equals` with another bound. |
| `unique(resource, *, by, **where)` | `unique(resource, { by, where? })` | two matching records share the same values for the field(s) in `by` (a name or a list). |
| `idempotent(resource, *, key="idempotency_key", **where)` | `idempotent(resource, { key?, where? })` | a matching record has no value in `key` (the record field that stores the idempotency key), or two share one. |

`where` is exact-match filters on record fields. The INC-1042 double refund fails `count` with exactly:

```text
effect violated: 2 refunds for O-1042 (expected 1)
```

A filter on several fields renders as `for amount_cents=4999, order_id=O-1042`, and a count of one drops the plural. The same agent sending the same `Idempotency-Key` on both POSTs leaves one record and passes. `world(None)` / `world(null)` (a test that ran without a world helper) is `not_evaluated` with reason `missing_evidence`. The wire format is in the "Layer 3 — Effect Checks" section of `proto/attest/v1/protocol-spec.md`.

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

## Limits

- HTTP and JSON only: collections with create, list and get. No updates, deletes, nested resources, auth, streaming or arbitrary handlers. A world is not a general service-virtualization platform; for anything it cannot express, write a real fake.
- A world replaces an external HTTP system. In-process `mock_tools` still stand in for tool functions and are not replaced.
- At most 32 resources, 10,000 records, 8 MiB of state, 1 MiB per request body and 256 KiB of `world.yaml`.
- The world listens on `127.0.0.1` under a random path token and serves nothing else.

See also: [capture](capture.md), [replay](replay.md), [behavior contracts](behavior-contracts.md).
