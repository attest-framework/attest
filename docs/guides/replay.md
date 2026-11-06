# Replay: run the agent's real code with zero model calls

A fixture captured by [`attest capture`](capture.md) holds every model exchange the run made. Replay serves those recorded responses to your agent's real code through a local proxy, so a test runs the agent end to end, in CI, with no provider key and no live call. Each test ends in one of three ways:

| Result | When |
| --- | --- |
| pass / fail | Every model request matched a recording; the test's assertions decide. |
| fail, with the changed component named | A request no longer matches (`strict`). |
| `not_evaluated: replay_miss` (skipped) | A request no longer matches, the mode is `auto` and no provider key is set. |

## Bind a test to a fixture

Python (pytest):

```python
def test_inc_1042_replay(attest_replay):
    with attest_replay("inc-1042"):
        result = run_refund_agent("O-1042")     # construct model clients here, after the call
    assert_double_refund_reproduces(result)
```

TypeScript (Vitest):

```ts
import { it } from "vitest";
import { useReplay } from "@attest-ai/vitest";

it("replays inc-1042", async () => {
  await useReplay("inc-1042");
  const result = await runRefundAgent("O-1042");  // construct model clients after the call
  // assertions
});
```

`attest_replay("<name>")` and `useReplay("<name>")` start `attest-engine replay --serve` for the fixture `tests/attest/fixtures/<name>.attest.json` and point `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` at it until the test ends (in Python, until the end of the `with` block when you use one). The official OpenAI and Anthropic SDKs read these variables when the client is constructed, so construct the client after the call. A client built with a hard-coded `base_url` bypasses the proxy: that is a live call, and the [egress guard](#block-direct-provider-connections) fails it.

To also host the external system the agent acts on, use a [world](world-fixtures.md#compose-with-replay) in the same test.

Both helpers take `mode`, `fixtures_dir` / `fixturesDir` and `engine_path` / `enginePath`. Only one replay runs at a time in a process, because it sets process-wide environment variables.

## Modes

| Mode | A request with no recording |
| --- | --- |
| `strict` | The proxy answers the agent with an error and the test fails with the miss message, whatever exception the agent raised and whatever marker the test carries: a miss is never an expected failure, so `pytest.mark.xfail(strict=True)` and Vitest `it.fails` do not turn it into XFAIL or a pass. |
| `auto` | When the request carries exactly the `OPENAI_API_KEY` (OpenAI requests) or `ANTHROPIC_API_KEY` (Anthropic requests) of the engine's environment, the proxy forwards it live and counts the call; a request with any other credential, or with the key unset, is never forwarded. The guard below (`block`) also forbids it. Without a key the test is `not_evaluated: replay_miss`: skipped, or failed under `--attest-strict` / `ATTEST_STRICT=1`. |
| `off` | Replay is disabled: the helper starts nothing and the agent calls the model as it normally would. |

Choose the mode with `pytest --attest-replay=strict|auto|off`, the `mode` argument, or `ATTEST_REPLAY` (Vitest reads only the argument and `ATTEST_REPLAY`). The default is `strict` when `CI` is set and `auto` otherwise, so a pull request check never goes live by accident and a local run can.

## What a miss says

The proxy compares the request that matched nothing with the next unused recording, one component at a time, and names the first that differs: the system prompt, the other messages, the tools, the parameters, then the model.

```text
replay miss: system prompt changed (hash a1b2c3d→e4f5a6b); re-record with `attest capture --refresh inc-1042`
```

The hashes are the first seven characters of a hash of that component, so you can see that it changed without the text appearing in CI logs. Other misses read `messages changed`, `tools changed`, `params changed`, `model changed ("gpt-4.1"→"gpt-5.4")` (model names are quoted, and control characters in any miss text are escaped), `the agent made more model calls than the 3 recorded`, or `the agent made an extra identical call`.

`attest capture --refresh inc-1042` prints the command recorded in the fixture (`source.command`), asks you to confirm it (`y` at a terminal; without one, `--yes`), re-runs it with its original flags (`--expect-fail` included) and overwrites the fixture. **A fixture is a file in your repository: review the printed command before you confirm a refresh of a fixture you did not record**, since whoever can change the file can change what runs. Review the new fixture in the diff like any other change.

## How requests are matched

A request matches a recording when its model, parameters, prompt (the system prompt and messages) and tool schemas are equal. Before comparing, replay redacts secrets in the live request the way capture did (including the values in the working directory's `.env`; an unusable `.env`, one that is not a regular file or is over 1 MiB, is ignored with one warning on stderr), then renumbers every volatile value in it (provider ids, timestamps, emails) by first occurrence, in the recording and in the request alike. So an id the agent generates today matches the placeholder stored in the fixture, and an id the replayed response handed back (`call_{{id_1}}`) matches too. Identical requests consume successive recordings in order, so an agent that retries gets the retry's recorded response. Claude Code runs need two more rules. `metadata.user_id` (device id, account uuid, session id) is not part of the key, so a run in a new session hits. The volatile values Claude Code writes about the run (working directory, platform, shell, OS, model name, today's date, git status, additional directories, `<total_tokens>` lines) are neutralized for matching in its `# Environment` section, `<env>` blocks, `# currentDate`, `gitStatus:` and `<system-reminder>` blocks; the stored request is unchanged, and any other line still counts. Contents of a `tool_result` are never normalized: a volatile value that reaches the request inside one (a `<total_tokens>` reminder, for example) must match the recording, or the request is a miss. See [capture](capture.md#templating).

A value the agent puts in a request that capture did not template (an order number, a free-text field) must be the same as in the recording; a change there is a miss.

Streamed responses replay as the recorded server-sent events. Placeholders stay in replayed bodies as recorded (`chatcmpl-{{id_1}}`); SDKs accept them as opaque ids.

## Block direct provider connections

`pytest --attest-egress=block`, `ATTEST_EGRESS=block`, or `useReplay(name, { egress: "block" })` makes connections from the test process to known provider hosts fail with `attest egress guard: connection to … blocked` (`EgressBlockedError`). Loopback and every other host are untouched. With the guard on, the replay proxy is also started with `--no-live`, so an `auto` miss is `not_evaluated: replay_miss` even when a key is set, and a run that records a live call while blocking fails. Installs are counted: a guard turned on by the runner stays on until every user releases it. Use it in the job that must prove zero live calls.

Blocked hosts: `api.openai.com`, `api.anthropic.com`, `generativelanguage.googleapis.com`, `aiplatform.googleapis.com` and `*-aiplatform.googleapis.com` (Vertex AI), `*.openai.azure.com` (Azure OpenAI), `bedrock-runtime.<region>.amazonaws.com` (AWS Bedrock), `api.mistral.ai`, `api.groq.com`, `openrouter.ai`, and `api.together.xyz` / `api.together.ai`.

**Proxies.** A call through a proxy connects to the proxy, not the provider, so the guard also screens the requests a client sends to the proxy named by `HTTPS_PROXY`, `HTTP_PROXY` or `ALL_PROXY` (either case, read when the socket connects): the target of every `CONNECT` and the host of every absolute-form request line (`GET http://host/…`), plus the `Host` header. A request that names a provider host fails the same way as a direct connection, in both SDKs. `NO_PROXY` is not read: a client that honors it connects to the provider directly, and the host check blocks that connection.

**Limits.** The guard is a best-effort tripwire for trusted test code, not an isolation boundary.

- It covers the process that runs your tests. Child processes are not inspected: the engine subprocess and anything the agent spawns keep their own network access.
- It matches the host name a client connects to. A client that connects to a literal IP address is not caught. (Python) A client that resolves names without `socket.getaddrinfo`, such as `uvloop` or `aiodns`, is not caught. (TypeScript) A resolver outside `net.Socket` is not caught.
- SOCKS proxies, an `https://` proxy connection (TLS hides the request line) and a proxy set only through a client option, not the environment, are not inspected.
- After an allowed `CONNECT` the guard goes opaque on a three-byte TLS record prefix (`0x16 0x03 0x00`–`0x04`) and does not check what the tunnel carries, and a client that forges the prefix and then writes plaintext is not inspected. Anything else after the `CONNECT` (the proxy refused it, or plain HTTP follows) is screened request by request again.

For a hard guarantee, run the job without network access or provider keys.

## Reports

The summary line counts the agent's own calls through the proxy:

```text
attest replay: 4 replayed · 0 live model calls · $0.00
```

`$` prices live calls whose model is in the engine's built-in price table; others show as `(+N unpriced)`. In `--attest-report` and the Vitest reporter output (report-v2), each replayed test has `replay`: `{ fixtures, mode, replayed, live, misses, miss? }`, and the summary has `replay` totals with `live_cost_usd`. An `auto` miss without a key adds a `not_evaluated` row with reason `replay_miss`, so `attest policy evaluate` reports it as not evaluated (exit 2 unless the policy blocks it). `summary.usage` keeps counting only the model calls attest itself makes (judges and embeddings).

`attest report summary --report attest-report.json` turns the report into the pull-request summary: the same `N replayed · N live model calls · $X` line, with replay misses listed at their file and line. A strict miss fails its test and appears as a failure row; see [policies](policies.md#render-the-report-for-reviewers). To run only the tests a changed fixture affects, see [selection](selection.md).

## Replaying the judge

Attest's own L6 integration tests run the engine's judge from fixtures: with the engine started under `ATTEST_JUDGE_PROVIDER=openai`, `ATTEST_JUDGE_BASE_URL` set to the replay session's `OPENAI_BASE_URL` and any non-empty `ATTEST_JUDGE_API_KEY`, `passes_judge` sends its request to the proxy and reads its verdict from the fixture. Run it in `strict` mode, so a changed rubric is a failure naming the change and nothing is sent to a provider. See `sdks/python/tests/integration/test_judge_replay.py`. The fixtures there were recorded against a scripted judge; `make record-judge-fixtures` records them against a real provider (it needs `ATTEST_JUDGE_API_KEY` or `OPENAI_API_KEY` and `ATTEST_JUDGE_MODEL`, and refuses to run without them).

## Command line

`attest-engine replay --name inc-1042 -- <command>` runs a command against the replay proxy, prints the summary and exits 1 on a miss or a failing command. `--serve` is the protocol the SDK helpers use: the engine prints `{"env": {...}}`, serves until a `stop` line (or end of input) arrives on stdin, then prints `{"summary": {...}}`.
