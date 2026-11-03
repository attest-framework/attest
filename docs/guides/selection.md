# Test selection

`attest select` lists the tests a change needs to run, so a pull request that touches one fixture does not run the whole suite. It reads the git diff and decides from file names alone: no model, no test execution, the same answer for the same diff.

```bash
uv run attest select --base origin/main
```

It diffs the merge base of `<ref>` and `HEAD` against the working tree, so committed changes, uncommitted changes and untracked files are all in. It prints one test path per line (sorted), the word `all`, or nothing when no test needs to run. Exit 2 means git or the ref failed; the error is on stderr.

`--json` prints the reasoning:

```json
{"mode":"subset","tests":["tests/test_refund.py"],"reasons":[{"path":"tests/attest/fixtures/inc-1042.attest.json","class":"fixture"}]}
```

`mode` is `subset` or `all` (`tests` is empty for `all`). Each reason is a changed path and its class. `--repo <dir>` selects another repository; paths are relative to it.

## Rules

| Changed path | Class | Selection |
|---|---|---|
| pytest `test_*.py`, `*_test.py`; Vitest `*.test.ts`, `*.test.tsx`, `*.test.js`, `*.test.mjs` | test | that file, if it still exists |
| `conftest.py` | test | every test file below its directory |
| `*.attest.json` | fixture | the test files that reference the fixture (below) |
| `*.md` | ignored | nothing |
| `uv.lock`, `poetry.lock`, `requirements*.txt`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lockb`, `bun.lock`, `pyproject.toml`, `package.json` | all | `all` |
| `attest.policy.yaml` and other `attest.*.yaml/yml/json/toml`, `world.yaml`, `*.world.yaml` | all | `all` |
| anything else | unknown | `all` |

A change to the Attest version is a change to a manifest or lockfile, so it selects `all`. Source files are unknown on purpose: the diff cannot say which tests exercise `src/agent.py`, so a source change runs everything. Selection only narrows a change that touches nothing but tests, fixtures and docs.

## Which tests reference a fixture

A test references a fixture when its source holds the fixture's capture name (the `name` field inside the fixture file) as a quoted string, as in `attest_replay("inc-1042")` or `useReplay("inc-1042")`, or a string that names the file (`fixtures/inc-1042.attest.json`). The match is on text, so it also finds names held in a constant defined in the same file, but not names built at runtime from pieces.

A fixture no test references gives `all`: selection cannot tell what depends on it. So does a fixture that was deleted or that has no readable name.

## Run the selection

```bash
tests=$(uv run attest select --base origin/main)
if [ "$tests" = "all" ]; then
  uv run pytest
elif [ -n "$tests" ]; then
  uv run pytest $tests
else
  echo "no tests selected"
fi
```

For Vitest, run `npx vitest run $tests` the same way. In GitHub Actions, fetch the base: `actions/checkout` with `fetch-depth: 0`, so the merge base exists.

`attest init --ci github` does not use selection; the generated workflow runs the full suite. Add the step above where a suite is slow enough to need it, and keep a full run on `main` so a selection error cannot hide a failure for long.
