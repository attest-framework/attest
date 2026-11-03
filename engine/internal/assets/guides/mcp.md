# MCP server

`attest mcp` gives a coding agent (Claude Code, Cursor, Codex) six tools over the Model Context Protocol on stdio. The agent drafts behavior contracts from a captured run, iterates them against it, reads failures and gets the test lines to paste. The server is read-only and offline: it writes no files, opens no sockets and makes no model calls.

## Set up a client

```bash
uv run attest init --mcp      # Python
npx attest init --mcp         # TypeScript
attest-engine mcp install --dir . --command '["attest","mcp"]'   # without an SDK
```

`init --mcp` calls `attest-engine mcp install` with the launch command for the project: `uv run attest mcp` when `uv.lock` is in the project root, else `attest mcp` (Python); `pnpm exec attest mcp` with `pnpm-lock.yaml`, else `npx --no attest mcp` (TypeScript; `--no` makes a missing local install fail instead of letting npx fetch the unscoped `attest` package from the registry). It writes or merges an `attest` entry into:

| Client | File | Entry |
|---|---|---|
| Claude Code | `.mcp.json` | `{"mcpServers": {"attest": {"command": "uv", "args": ["run", "attest", "mcp"]}}}` |
| Cursor | `.cursor/mcp.json` | the same, with `"type": "stdio"` |
| Codex | `.codex/config.toml` (trusted projects) | `[mcp_servers.attest]` with `command` and `args` |

Other servers and keys in those files are kept. A different existing `attest` entry is reported as `conflict` and left alone; `--force` replaces it, except in `.codex/config.toml` when the entry is written as an inline table or dotted keys rather than a `[mcp_servers.attest]` table, which stays a conflict for you to edit. The Codex file is parsed as TOML: invalid TOML, or JSON with trailing content after the object, is refused and nothing is written. Existing files keep their permission bits; files are replaced through a synced temporary file. Every destination is validated before anything is written, and a destination that resolves outside the project directory is refused. `init --json` lists written files as `created` or `updated`, unchanged ones as `skipped`, and with `--mcp` adds a `conflict` list; the text output labels a conflict as such and names `attest-engine mcp install --force`.

Claude Code asks you to approve a project-scoped server before using it. Codex reads `.codex/config.toml` only in a project it trusts.

## Run the server

```bash
attest-engine mcp [--root DIR]     # serve on stdio; the root defaults to the working directory
attest-engine mcp --tools --json   # print the fixed tool list and exit
```

Stdout carries only protocol frames; logs go to stderr. A call has a 30 s deadline, at most four calls run at once, and a panic while running a tool is reported as a tool error.

### Which directory is the project root

The root is `--root`, else the directory the client starts the server in. The vendors do not document that directory for stdio servers: Claude Code documents a working directory only for `headersHelper` (the project directory for a project `.mcp.json`), Cursor's table has no `cwd` option (it offers `${workspaceFolder}` as a variable), and Codex has an optional `cwd` setting but does not state the default. Without an explicit `--root`, the server refuses to start in `/` or in your home directory (exit 2). To pin the root, add it to the launch command, for example `attest mcp --root /path/to/project`, or set `cwd` in `.codex/config.toml`. `mcp --json` without `--tools` is a usage error (exit 2).

## Tools

The toolset is fixed: it is registered once at startup from a static table, and `tools/list` returns the same answer on every call, in every session and in every project. `mcp --tools --json` prints the same table with each tool's input and output schema (`cli/mcp-tools.schema.json`).

Every path argument is relative to the project root or absolute inside it.

| Tool | Input | Returns |
|---|---|---|
| `trace_check` | `path` | The `attest trace check --json` report for an OTLP/JSON export or a capture fixture. |
| `evaluate` | one of `trace_path`, `fixture_path`, `trace` (inline trace-v2), and `assertions`: `[{assertion_id, type, spec}]` | One result per assertion with a status of `pass`, `soft_fail`, `hard_fail` or `not_evaluated`. |
| `explain_failure` | `report_path`, optional `test_id` | Every failing or not-evaluated row of a report-v2 file: status, reason, explanation, failure class, file and line, the `attest guide <topic>` to read and a one-line next step. |
| `draft_contract` | `fixture_path` or `trace_path`, optional `expect_fail` | Contracts drafted from the run and verified on it, each as a `run_contract` statement with its spec, status and the Python and TypeScript test lines. |
| `run_contract` | `statements`, one of `fixture_path` / `trace_path`, optional `world_path` | Per statement: status, explanation, spec, and the Python and TypeScript test lines (effect lines start with `.world(FIXTURE.world)`, also returned as `chain_prefix`). |
| `fixture_info` | `path` | Name, how the fixture was captured, trace and exchange counts, contracts, world summary, verification and a redaction check. |

`evaluate` and `run_contract` run the deterministic layers only: schema, constraint, trace, content, trace tree and effect checks. Embedding and judge assertions return `not_evaluated` with reason `provider_not_configured`, because the server never calls a model or the network.

A tool failure (a path outside the root, an unknown field, a statement list that is too long) is a tool result with `isError: true` and a message that names the problem, never the file contents.

### Example: iterate the INC-1042 contract

`draft_contract` on the incident fixture returns the contracts the run supports, each ready to run:

```json
{
  "id": "cardinality-issue_refund-per-order_id",
  "statement": "tool_cardinality(\"issue_refund\", per=\"order_id\", at_most=1)",
  "status": "hard_fail",
  "explanation": "issue_refund called 2× for order_id=O-1 (max 1)",
  "python": ".tool_cardinality(\"issue_refund\", per=\"order_id\", at_most=1)",
  "typescript": ".toolCardinality(\"issue_refund\", { per: \"order_id\", atMost: 1 })"
}
```

`run_contract` takes statements the agent writes or edits:

```json
{
  "fixture_path": "tests/attest/fixtures/inc-1042.attest.json",
  "statements": [
    "trajectory([\"lookup_order\", \"issue_refund\"], mode=\"superset\")",
    "tool_cardinality(\"issue_refund\", per=\"order_id\", at_most=1)",
    "__import__(\"os\")"
  ]
}
```

```json
{
  "summary": {"total": 3, "pass": 1, "soft_fail": 0, "hard_fail": 1, "not_evaluated": 0, "error": 1},
  "results": [
    {"status": "pass", "explanation": "trajectory (superset) matches [lookup_order issue_refund]"},
    {"status": "hard_fail", "explanation": "issue_refund called 2× for order_id=O-1 (max 1)",
     "python": ".tool_cardinality(\"issue_refund\", per=\"order_id\", at_most=1)"},
    {"status": "error", "explanation": "unknown method \"__import__\" (position 1)"}
  ]
}
```

A statement that does not parse is an `error` row for that statement, not a protocol error; the other statements still run. Paste the `python` or `typescript` line of a passing row into the contract chain of a test.

## Contract statements

A statement is one call in Python call syntax with literal arguments. The parser is a hand-written lexer and recursive-descent parser; nothing is evaluated.

Arguments are literals only: strings in `"` or `'` (escapes `\\ \" \' \n \t`), integers, floats, `True`/`False`/`None` (also `true`/`false`/`null`), lists, and maps with string keys. Keyword arguments follow positional ones. Limits: 8 KiB per statement, 8 levels of nesting, 50 statements per call.

Everything else is rejected with a position: identifiers as values, attribute access, operators, calls in arguments, f-strings, comments, lambdas, imports, a second call, unknown methods, unknown or repeated keyword arguments, and wrong argument types.

The methods are a closed catalog. Each is accepted under its Python name or its TypeScript name (`tool_cardinality` or `toolCardinality`), with keywords in snake_case or camelCase.

| Method | Arguments | Layer |
|---|---|---|
| `tool_cardinality` | `tool`, `per`, `at_most`, `at_least`, `soft` (one bound required) | trace |
| `forbidden_tools` | `tools`, `soft` | trace |
| `trajectory` | `tools`, `mode` (`strict`, `unordered`, `subset`, `superset`), `soft` | trace |
| `trajectory_partial_order` | `order` (list of `[before, after]`), `tools`, `soft` | trace |
| `tool_called_before` | `before`, `after`, `soft` | trace |
| `no_tool_cycles` | `period`, `min_repeats`, `soft` | trace |
| `max_tool_failure_streak` | `max_streak`, `soft` | trace |
| `tool_arg_satisfies` | `tool`, `arg`, `op`, `value`, `soft` | trace |
| `output_matches_schema` | `schema` | schema |
| `cost_under` | `max_cost`, `soft` | constraint |
| `count` | `resource`, `equals` or `at_most` / `at_least`, filters | effect |
| `unique` | `resource`, `by`, filters | effect |
| `idempotent` | `resource`, `key`, filters | effect |

Effect methods check the world: the fixture's own, or the snapshot in `world_path` (a world-v1 file, or another fixture). Extra keyword arguments on an effect method are exact-match record filters, as in the SDKs (`count("refunds", equals=1, order_id="O-1042")`); use `where={...}` for a filter named like a parameter. Without a world they return `not_evaluated` with reason `missing_evidence`.

The grammar and the SDK DSL share one spec: tests check every case in `protocol-tests/contracts/dsl.json` and `effect-dsl.json`, so a statement produces the spec the Python and TypeScript calls produce.

## Confinement

The project root is the working directory or `--root`. Every path is checked lexically first (`..` and absolute paths outside the root are refused before any file is touched) and then opened through the root directory, so each path component, symlinks included, is resolved inside the root at open time: a symlink that leads out, a chain of them, or one swapped in after a check cannot escape. A symlink to an absolute path is refused even when its target is inside the root; use a relative link. Only regular files are read, at most 32 MiB each; a larger file, a directory or a named pipe is refused. A path that cannot be read inside the root gets one message, whether or not it exists outside, so an agent cannot probe the filesystem. Errors about a file inside the root can still quote a short fragment of it, such as a JSON parse error; paths the agent supplied are echoed back.

The server writes nothing: there is no tool that creates or edits a file. It opens no listener and makes no outbound request; a test fails if `evaluate` with a judge or embedding assertion makes an HTTP call.

The same path guard backs `attest skills install` and `attest mcp install`.

## Reference

Exit codes and the `--json` schemas are in the [CLI reference](cli.md). Capture and replay: [capture](capture.md), [replay](replay.md). Contract DSL: [behavior contracts](behavior-contracts.md).
