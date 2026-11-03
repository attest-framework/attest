# Agent skills: let coding agents operate Attest

Attest ships agent skills, in the Agent Skills format (a directory with a `SKILL.md` that has `name` and `description` frontmatter), that teach a coding agent such as Claude Code, Codex or Cursor to write contracts, capture incidents, debug failures, set up the merge gate and migrate eval suites. The skills are embedded in `attest-engine`, so they match the installed version and need no network. They hold procedures only: the reference material stays in these guides, which the agent reads with `attest guide <topic>`.

```bash
uv add "attest-ai>=1.0.0rc1"
npm install -D "@attest-ai/core@>=1.0.0-rc.1" "@attest-ai/vitest@>=1.0.0-rc.1" vitest
```

## The skills

| Skill | Use it to |
| --- | --- |
| `attest` | Route a task to one of five procedures: write a behavior or world-effect contract for an incident (`references/write-contracts.md`), capture a run or incident as a fixture (`capture-incident.md`), debug a failing or not-evaluated test (`debug-failure.md`), set up the CI merge gate (`ci-setup.md`), and migrate from Promptfoo or DeepEval while keeping a `MIGRATION.md` progress file in your repository (`migrate.md`). It also sets the rules: zero model calls in tests and CI, no secrets written anywhere, `--json` for parsed output, and `not_evaluated` reported with its reason, never turned into a pass. |
| `attest-verify-generator` | Inspect your repository (agent entry point, tools, side effects, incidents, fixtures, worlds, test and CI commands) and write a project-specific `verify-<agent>` skill to `.agents/skills/verify-<agent>/SKILL.md` and `.claude/skills/verify-<agent>/SKILL.md`, listing the exact offline commands that verify that agent and the contracts to extend. |

## Install with `attest init --skills`

```bash
uv run attest init --skills
npx attest init --skills
```

`--skills` installs the skills through the engine, then `init` scaffolds as usual: the sample test and `attest.policy.yaml` when they do not exist. Combine it with the CI workflow:

```bash
uv run attest init --skills --ci github --json
```

`--json` prints one document, paths relative to the project root (abbreviated here):

```json
{"created": [".agents/skills/attest/SKILL.md", ".claude/skills/attest/SKILL.md", "attest.policy.yaml"], "updated": [], "skipped": ["tests/test_my_agent.py"]}
```

`skills install` validates every destination before it writes anything, so a refusal leaves the project untouched (exit 1). It overwrites only the files of the embedded skills. A symlinked `.claude`, `.agents`, `.claude/skills` or `.agents/skills` (or any deeper directory) is followed when its fully resolved target stays inside the `--dir` root; files behind the same resolved path are written once and reported under each logical path (for example `.claude/skills -> ../.agents/skills`). A symlink that resolves outside the root, a symlinked skill file and any other existing non-regular file are refused.

`skipped` lists files that already existed (scaffold files are never overwritten) and skill files already up to date. Without an engine that provides `skills`, `init --skills` exits 1 and scaffolds nothing; a usage error exits 2.

## Install with `attest skills`

To install only the skills, without scaffolding:

```bash
uv run attest skills list --json              # {"skills": [{"name", "description"}, ...]}
uv run attest skills install                  # into the current directory
uv run attest skills install --dir path/to/repo --json
```

`install` writes every file of every shipped skill to two places under the directory:

| Directory | Read by |
| --- | --- |
| `.agents/skills/<name>/` | Codex, Cursor and other agents that read Agent Skills from `.agents/skills/` |
| `.claude/skills/<name>/` | Claude Code |

It writes only the files of the shipped skills (`attest`, `attest-verify-generator`), so other skills in those directories, including generated `verify-<agent>` skills, are never touched, and it deletes nothing. Running it again is idempotent; after an upgrade it updates the changed files. `--json` prints `{dir, created, updated, unchanged}`. Exit 0: installed; 1: a file could not be written; 2: usage error. Commit both directories so every contributor's agent gets the same version.

## Install as a plugin

The repository `attest-framework/attest` is also a plugin for each agent, publishing the same `skills/` directory.

Claude Code:

```text
/plugin marketplace add attest-framework/attest
/plugin install attest@attest-framework
```

Cursor reads `.cursor-plugin/plugin.json` and Codex reads `.codex-plugin/plugin.json` at the repository root; install the plugin from `attest-framework/attest` with the agent's plugin command. `attest init --skills` installs the skills of the SDK version pinned in your project; a plugin install tracks the plugin repository instead.

## How an agent uses the CLI

The skills tell the agent to discover instead of guessing:

| Command | Gives the agent |
| --- | --- |
| `attest doctor --json` | engine version, capabilities and provider configuration (no key values) |
| `attest help --json` | every command with its summary, subcommands, the schema of its `--json` output and its exit codes |
| `attest guide` | the topics: one per guide, with its title |
| `attest guide <topic>` | the guide's Markdown, offline; `--json` gives `{topic, title, markdown}`. An unknown topic exits 2 and lists the topics |

`attest` is `uv run attest` or `npx attest`; both forward to the engine of the installed SDK version. Every command accepts `--json` and then writes exactly one JSON document to stdout on success, with diagnostics on stderr; on a usage, load or runtime error it writes nothing to stdout, and the exit code plus stderr describe the error. Exit codes are 0 success, 1 a failed result, 2 usage error; `attest policy evaluate` exits 0 pass, 1 block, 2 warn, 3 usage or load error. Nothing prompts in CI, under `--json` or without a terminal: `attest capture --refresh` refuses there unless given `--yes`. See [cli](cli.md) for the full command and exit-code reference.

## Ask your agent

With the skills installed, ask in plain terms; the `attest` skill routes the request:

```text
Add a contract that prevents duplicate refunds for the INC-1042 incident and make the test pass without calling a model.
```

```text
Migrate our Promptfoo suite to attest and track progress in MIGRATION.md.
```

```text
Generate a verify skill for our support agent.
```

See also: [behavior contracts](behavior-contracts.md), [capture](capture.md), [replay](replay.md), [world fixtures](world-fixtures.md), [merge gate](merge-gate.md).
