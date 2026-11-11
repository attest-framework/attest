---
name: attest-verify-generator
description: Generate a project-specific verify-<agent> skill that tells future coding agents how to verify this repository's AI agent offline with attest (exact commands, fixtures, worlds, contracts to extend). Use when asked to create a verify skill, onboard attest for an agent, or document how to test an agent without model calls.
---

# Generate a verify-<agent> skill

Output: one skill per agent, written twice with identical content:

- `.agents/skills/verify-<agent>/SKILL.md` (Codex, Cursor and other Agent Skills readers)
- `.claude/skills/verify-<agent>/SKILL.md` (Claude Code)

`<agent>` is a lowercase kebab-case slug of the agent's name (example: `shop-assistant`). The frontmatter `name` must equal the directory name `verify-<agent>`. Use the template in `references/template.md`. General attest procedures live in the `attest` skill; the generated skill holds only this repo's facts and commands.

## 1. Discover

Run these from the repository root and keep the output; every fact in the generated skill must come from a file or a command run here.

```bash
# language, package manager, runner
ls pyproject.toml uv.lock package.json package-lock.json pnpm-lock.yaml yarn.lock bun.lock bun.lockb Makefile 2>/dev/null
uv run attest doctor --json 2>/dev/null || npx attest doctor --json

# agent entry points and model clients
git grep -n -E 'OpenAI\(|AsyncOpenAI\(|Anthropic\(|new OpenAI|new Anthropic|chat\.completions\.create|responses\.create|messages\.create'
git grep -n -E 'OPENAI_BASE_URL|ANTHROPIC_BASE_URL|base_url=|baseURL'

# tools and their arguments
git ls-files '*tools*.json' '*tools*.yaml'
git grep -n -E '"type": "function"|tool_choice|tools=\[|@tool\b|function_tool|def execute_tool'

# side effects (HTTP, databases, queues) and their configuration
git grep -n -E 'httpx\.|requests\.(post|put|patch|delete)|fetch\(|axios|_BASE_URL|Idempotency-Key'

# tracing
git grep -n -E 'opentelemetry|OTEL_EXPORTER_OTLP|InMemorySpanExporter|OTelAdapter'

# attest material: fixtures, worlds, incidents, existing attest tests, policy
git ls-files '*.attest.json' '*world.yaml' '*.otlp.json' 'incidents/*' '*attest.policy.yaml'
git grep -l -E 'attest_replay|useReplay|attest_world|useWorld|attest\.evaluate|from attest|@attest-ai'

# how tests and CI run, with which environment
git ls-files '.github/workflows/*' '.env.example' '*.mk'
git grep -n -E 'pytest|vitest|ATTEST_|--attest-' -- Makefile '*.mk' package.json '.github/workflows/*'
```

## 2. Interview

Fill this table. Ask the user only for rows the discovery could not answer, in one message. When no user can answer (non-interactive run), choose the most specific answer the files support and list it under "Assumptions" in the generated skill.

| Question | Answer source |
| --- | --- |
| Which agent (if several) and its entry point a test can call | entry-point grep, existing tests |
| Its tools and the business key in their arguments (`order_id`, `user_id`) | tool schemas |
| Side effects that must never repeat or must never happen | HTTP calls, incident write-ups, user |
| Known incidents and the rule each one broke | `incidents/`, issues, user |
| Fixtures and what run each recorded (incident with `--expect-fail`, or a good run) | `*.attest.json` `name`, `verification.expect_fail`, `source.command` |
| Does each test rerun the current agent or only inspect a frozen recording? | command-capture drafted tests use `replay_run` / `replayRun`; commandless captures load fixture traces |
| Worlds and the base URL variable each sets | `*.world.yaml` `base_url_env` |
| Environment the agent requires and safe placeholder values | settings code, `.env.example`, Makefile |
| The exact test commands, and which run without model calls | Makefile, package.json scripts, CI workflow |
| Upstream a capture would call (scripted local server or a paid provider) | `OPENAI_BASE_URL` in Makefile or env files |

Never copy secret values into the skill, even from `.env` files; name the variable and give a placeholder.

## 3. Verify the commands

Run every command the generated skill will list. A command goes in the "Run" section only if it ran with zero live model calls (`0 live model calls` in the replay line, or no model client involved) and its result is recorded. A command that needs a model, a missing world or missing setup goes under "Gaps" with the reason. Do not call paid models to fill a gap.

For Attest 1.1.0, verify that every chain is evaluated (again after adding assertions), not just constructed. A green frozen-incident `inspect()` test is reproduction evidence, not a guard on the current code. For TypeScript, run `npx attest doctor` before the first Vitest run to download and check the engine outside the first test's timeout.

If no fixture or contract exists yet, say so under "Gaps" and point to the `attest` skill (`references/capture-incident.md`, `references/write-contracts.md`) instead of inventing commands.

## 4. Write both files

1. Render `references/template.md` with the facts. Remove optional rows that do not apply; never leave a `<placeholder>`.
2. Write `.agents/skills/verify-<agent>/SKILL.md` and copy it byte for byte to `.claude/skills/verify-<agent>/SKILL.md`.
3. Check: `cmp .agents/skills/verify-<agent>/SKILL.md .claude/skills/verify-<agent>/SKILL.md` exits 0, and the frontmatter parses (`name`, `description` present; `name` matches the directory).
4. Do not overwrite an existing `verify-<agent>` skill without showing the user the diff.

Report: the two paths, the commands verified with their result lines, and the gaps.
