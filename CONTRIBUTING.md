# Contributing to Attest

Thank you for your interest in contributing to Attest, the test framework for AI agents.

## Prerequisites

- **Go 1.27+** — [Install Go](https://go.dev/dl/)
- **Python 3.10+** — [Install Python](https://www.python.org/downloads/)
- **uv** — [Install uv](https://docs.astral.sh/uv/getting-started/installation/)

## Quick Start

```bash
# Clone the repository
git clone https://github.com/attest-framework/attest.git
cd attest

# Build the engine and install the Python SDK
make dev-setup

# Run all tests
make test
```

## Repository Layout

```text
attest/
├── proto/              # Protocol specification (JSON-RPC 2.0)
│   └── attest/v1/      # Protocol v1 spec documents
├── engine/             # Core engine (Go)
│   ├── cmd/            # CLI entrypoints
│   ├── internal/       # Private engine implementation
│   ├── pkg/            # Public Go packages
│   └── testdata/       # Shared test fixtures
├── sdks/
│   ├── python/         # Python SDK (PyPI: attest-ai)
│   ├── typescript/     # TypeScript SDK (npm: @attest-ai/core)
│   └── go/             # Go SDK
├── docs/               # Documentation
├── examples/           # Standalone example projects
└── scripts/            # Build and development scripts
```

## Development Workflow

### 1. Create a branch

```bash
git checkout -b feat/your-feature-name
```

### 2. Make changes

Follow the code standards below. Keep commits small and focused.

### 3. Run tests

```bash
# Engine tests
make engine-test

# Python SDK tests
make sdk-python-test

# All tests
make test
```

### 4. Lint and type check

```bash
# Engine
make engine-lint

# Python SDK
make sdk-python-lint
```

### 5. Submit a pull request

Push your branch and open a PR against `main`. CI runs automatically.

## Engine Development (Go)

### Building

```bash
make engine
# Binary output: bin/attest-engine
```

### Testing

```bash
make engine-test
# Runs: go test ./... -v -race
```

### Linting

```bash
make engine-lint
# Runs: go vet ./...
```

### Project structure

- `cmd/attest-engine/` — CLI entrypoint
- `internal/server/` — Protocol server (JSON-RPC over stdio)
- `internal/assertion/` — 6-layer assertion pipeline
- `internal/trace/` — Trace data model and normalization
- `internal/simulation/` — Simulation runtime
- `internal/llm/` — LLM client and provider integrations
- `internal/report/` — Report generation (JUnit, JSON, Markdown)
- `pkg/types/` — Exported protocol types

## Python SDK Development

### Setup

```bash
cd sdks/python
uv venv .venv
uv pip install -e ".[dev]"
```

### Testing

```bash
make sdk-python-test
# Or directly:
cd sdks/python && uv run pytest tests/ -v
```

### Linting and type checking

```bash
make sdk-python-lint
# Or directly:
cd sdks/python
uv run ruff check src/
uv run mypy src/attest/
```

### Package structure

- `src/attest/__init__.py` — Public API surface
- `src/attest/plugin.py` — pytest plugin registration
- `src/attest/adapters/` — Framework-specific trace adapters

## Protocol Changes

The protocol specification lives in `proto/attest/v1/protocol-spec.md`.

**Rules for protocol changes:**

1. New fields in existing messages are non-breaking
2. New methods are non-breaking
3. Changing field types, removing fields, or renaming fields is breaking
4. Breaking changes require a `protocol_version` bump
5. SDKs and engine MUST ignore unknown fields

When modifying the protocol:

1. Update `proto/attest/v1/protocol-spec.md`
2. Update engine implementation in `engine/internal/server/`
3. Update SDK implementations
4. Add tests covering the protocol change
5. Update capability identifiers if adding new functionality

## CI

CI runs automatically on pull requests as one workflow, `.github/workflows/ci.yml`. Its first job, `changes`, detects which paths the pull request touches, and each suite runs only when the paths it guards changed:

- **Engine** (`engine/**`, `proto/**`, `protocol-tests/**`, `docs/guides/**`, `skills/**`, plugin manifests, `VERSION`)
  - `go test -race`, `go vet`, `govulncheck`, `go build`
- **Python SDK** (`sdks/python/**`, `sdks/parity/**`, `protocol-tests/**`, `engine/**`, `proto/**`)
  - Matrix: Python 3.10, 3.11, 3.12
  - `pytest`, `ruff`, `mypy`, `pip-audit`
- **TypeScript SDK** (`sdks/typescript/**`, `sdks/parity/**`, `protocol-tests/**`, `engine/**`)
  - Matrix: Node 20, 22
  - build, test, `tsc --noEmit`
- **setup-attest** (`.github/actions/setup-attest/**`), **GitHub config** (`.github/**`), **version sources** (the files `scripts/verify-versions.sh` compares) and the **release dry run** (`release.yml`, the release scripts, `CHANGELOG.md`, `VERSION`)

The final job, `required`, always runs. It fails when any job it depends on failed or was cancelled, and passes when they succeeded or were skipped, so a pull request that touches only `README.md` runs `changes` and `required` and nothing else. `required` is the one status check to require in branch protection.

## Code Standards

### Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(engine): add Layer 3 trace ordering assertions
fix(sdk-python): handle empty trace output
docs(proto): clarify capability negotiation
test(engine): add constraint assertion edge cases
chore(ci): update Go version to 1.24
```

### Go

- Follow standard Go conventions (`go vet`, `gofmt`)
- Use `internal/` for private packages
- Write table-driven tests
- Handle errors explicitly — no silent swallowing

### Python

- Target Python 3.10+
- Use `from __future__ import annotations` in all files
- Use modern type syntax: `list[int]`, `str | None`
- Follow ruff rules: E, F, I, N, W, UP
- Pass mypy strict mode
- Use uv for all package management — never pip directly

## Security

### Reporting vulnerabilities

If you discover a security vulnerability, do NOT open a public issue. Email security@attest-ai.dev.

### Security scanning

```bash
make security
```

This runs `govulncheck` on the engine and `pip-audit` on the Python SDK.

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
