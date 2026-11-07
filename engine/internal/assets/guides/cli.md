# Agent-facing CLI

`attest` is built for coding agents as well as people. Every command is discoverable offline, prints one JSON document on request, and returns a documented exit code. `attest` in each SDK implements `init` itself and forwards every other command to `attest-engine`; the contract below applies to both spellings.

## Discover

<!-- snippet: fragment reason="`<topic>` is a placeholder for a guide name" -->
```bash
attest help --json      # every command, its schema, its exit codes
attest guide            # list the shipped guides
attest guide <topic>    # print one guide as Markdown, offline
```

`attest help --json` prints `{"commands": [{"name", "summary", "schema", "subcommands", "exit_codes"}]}`. `schema` and each subcommand's `schema` are paths relative to `proto/attest/v1/` in the attest repository. Run through the SDK (`uv run attest help --json`, `npx attest help --json`), the document also lists `init`, which the SDK implements itself (schema `cli/init.schema.json`; exit 0 success, 1 `--skills` or `--mcp` could not install, 2 usage error). The text form (`attest help`) is unchanged.

## JSON output

Every command and subcommand accepts `--json`. With `--json` a successful command writes exactly one JSON document to stdout and sends diagnostics to stderr. On a usage, load or runtime error the command writes nothing to stdout; the exit code and stderr describe the error. Errors are not JSON objects: read the exit code and stderr. Without it, output stays human-readable.

Commands that run a child process (`capture`, `replay`, `world` with `-- <command>`) send the child's stdout to stderr under `--json` and print one result object at exit; `exit_code` in that object is the child's exit code (`null` when a signal killed it). `--serve` already writes JSON lines, so `--serve --json` is a usage error (exit 2).

`attest init --json` prints `{"created": [...], "updated": [...], "skipped": [...]}` with paths relative to the project root; see `attest guide agents`.

| Command | Schema |
|---|---|
| `baseline pin` | `cli/baseline-pin.schema.json` |
| `baseline list` | `cli/baseline-list.schema.json` |
| `baseline show` | `cli/baseline-show.schema.json` |
| `baseline delete` | `cli/baseline-delete.schema.json` |
| `cache stats` | `cli/cache-stats.schema.json` |
| `cache clear` | `cli/cache-clear.schema.json` |
| `calibrate` | `cli/calibrate.schema.json` |
| `capabilities` | `cli/capabilities.schema.json` |
| `capture` | `cli/capture.schema.json` |
| `doctor` | `doctor.schema.json` |
| `guide` | `cli/guide.schema.json` |
| `mcp tools` (`mcp --tools --json`) | `cli/mcp-tools.schema.json` |
| `mcp install` | `cli/mcp-install.schema.json` |
| `mutate` | `cli/mutate.schema.json` |
| `policy evaluate` | `cli/policy-evaluate.schema.json` |
| `replay` | `cli/replay.schema.json` |
| `report junit` | `cli/report-junit.schema.json` |
| `report summary` | `cli/report-summary.schema.json` |
| `report rows` | `cli/report-rows.schema.json` |
| `select` | `cli/select.schema.json` |
| `skills list` | `cli/skills-list.schema.json` |
| `skills install` | `cli/skills-install.schema.json` |
| `trace check` | `cli/trace-check.schema.json` |
| `trace convert` | `cli/trace-convert.schema.json` |
| `version` | `cli/version.schema.json` |
| `view` | `cli/view.schema.json` |
| `world` | `cli/world.schema.json` |
| `help` | `cli/help.schema.json` |

`doctor` keeps its schema at `proto/attest/v1/doctor.schema.json`; `init` is `cli/init.schema.json`.

## Exit codes

Convention: 0 success, 1 the command ran and the result is a failure (check failed, block, child failed), 2 usage or invalid input. `policy evaluate` is the exception: its exit code is the verdict, so usage and load errors use 3 and cannot be mistaken for a warning or a block.

The table is checked against the engine's command registry by a test; a change to either side fails `go test ./...`.

| Command | Code | Meaning |
|---|---|---|
| `baseline` | 0 | success |
| `baseline` | 1 | the report could not be loaded or has no assertion results, the tag is not pinned (show), or the baseline store failed |
| `baseline` | 2 | usage error |
| `cache` | 0 | success |
| `cache` | 1 | the cache directory could not be read, or the home directory could not be determined |
| `cache` | 2 | usage error |
| `calibrate` | 0 | success |
| `calibrate` | 1 | the labels file could not be read, has no judge_score rows or no usable agreement, or persisting failed |
| `calibrate` | 2 | usage error |
| `capabilities` | 0 | success |
| `capabilities` | 1 | writing the output failed |
| `capabilities` | 2 | usage error |
| `capture` | 0 | the fixture is verified or captured with nothing to verify, or --check-redaction found no secrets; the command's own exit code is recorded, not propagated |
| `capture` | 1 | the fixture is not verified (a drafted contract failed, the trace check failed, no step was mapped, --expect-fail drafted nothing, or nothing was drafted while the command exited non-zero or recorded no model call) or was not written, the capture failed, or --check-redaction found secrets |
| `capture` | 2 | usage error, an invalid --world file, or a --refresh that was not confirmed |
| `doctor` | 0 | success |
| `doctor` | 1 | configuration error the JSON-RPC server would refuse to start with |
| `doctor` | 2 | usage error |
| `guide` | 0 | guide list or guide printed |
| `guide` | 1 | an embedded guide is unreadable or has no title |
| `guide` | 2 | unknown topic or usage error |
| `mcp` | 0 | the client disconnected, the tools were listed, or the client configs were written |
| `mcp` | 1 | the server failed, the project root is unusable, or a config could not be read or written |
| `mcp` | 2 | usage error |
| `mutate` | 0 | a mutation report was produced |
| `mutate` | 1 | no contract applies to any fixture, so there is no score |
| `mutate` | 2 | usage error, or a fixture or contracts directory could not be loaded |
| `policy` | 0 | pass: every rule satisfied |
| `policy` | 1 | block: a block-severity rule was violated |
| `policy` | 2 | warn: only warn-severity rules were violated |
| `policy` | 3 | usage error, or the policy, report, baseline or mutation report failed to load, or a rule lacks its input (block_on_regression or regression without --baseline, min_mutation_score without --mutation), or the report has 0 tests while tests_collected is above 0, or the baseline was pinned under id scheme 1 (re-pin it on 1.1), or the head report uses id scheme 1 (re-run with an SDK on 1.1) |
| `replay` | 0 | every request was served and the command exited 0 |
| `replay` | 1 | the command exited non-zero (its code is not propagated; --json reports it as exit_code), a request missed, or the fixture could not be loaded or served |
| `replay` | 2 | usage error or an invalid --world file |
| `report` | 0 | success |
| `report` | 2 | usage error, or unreadable or invalid input, or the output could not be written |
| `select` | 0 | success, including an empty selection |
| `select` | 2 | usage error, or git or the base ref failed |
| `skills` | 0 | skills listed or installed |
| `skills` | 1 | an embedded skill is invalid or a destination was refused or a file could not be written |
| `skills` | 2 | usage error |
| `trace` | 0 | success: the trace converted, or check found no structural errors |
| `trace` | 1 | check found structural errors, or the input could not be read, parsed or converted |
| `trace` | 2 | usage error |
| `version` | 0 | success |
| `version` | 1 | writing the output failed |
| `version` | 2 | usage error |
| `view` | 0 | success: the page was written |
| `view` | 2 | usage error, or unreadable or invalid input, or the page could not be written |
| `world` | 0 | the command exited 0, or --serve stopped cleanly |
| `world` | 1 | the command exited non-zero (its code is not propagated; --json reports it as exit_code), or the world could not be served or its state written |
| `world` | 2 | usage error or an invalid world.yaml |

## `replay --trace-out`

`attest-engine replay --trace-out <file> -- <command>` writes the trace of the new run (the spans the command exported over OTLP) to `<file>` as attest trace JSON. It is written when every request was served, whatever the command's exit status (an incident recording exits non-zero). The trace is **not redacted**: it holds what the command sent and received during that run. Write it to a private directory and do not commit it. A drafted replay test keeps it in a private temporary directory that is removed when the run ends. `--trace-out` needs `-- <command>`; it is a usage error with `--serve`.

## Non-interactive use

Nothing prompts under CI, `--json` or a non-TTY stdin. The only confirmation prompt (`attest capture` before it re-runs a command for `--refresh`) refuses with exit 2 and a `--yes` hint when `CI` is set, `--json` is given or stdin is not a terminal. Pass `--yes` to proceed. It is not the only place a recorded command runs: a drafted replay test runs the argv written in the test file itself (`COMMAND`, reviewed with the test) and refuses to run when the fixture's `source.command` differs from it, or has redacted or templated arguments. See [Capture](capture.md#the-drafted-test).

## Offline

`attest guide` and `attest skills` read assets embedded in the engine binary and make no network calls.
