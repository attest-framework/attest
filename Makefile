.PHONY: record-judge-fixtures all generate generate-claims check-claims engine engine-test engine-lint sdk-python sdk-python-test sdk-python-lint security test clean dev-setup verify-versions

# ── Version source-of-truth ──
verify-versions:
	bash scripts/verify-versions.sh

# ── Engine ──
# Stage docs/guides and skills into the engine for go:embed and write the
# plugin manifests; engine-test fails until this has been run after an edit.
generate:
	cd engine && go generate ./internal/assets/

engine: verify-versions
	cd engine && go build -o ../bin/attest-engine ./cmd/attest-engine/

# Render the README and guide regions from `attest-engine capabilities --json`.
generate-claims: engine
	python3 scripts/generate_claims.py --engine bin/attest-engine

check-claims: engine
	python3 scripts/generate_claims.py --check --engine bin/attest-engine

engine-test:
	cd engine && go test ./... -v -race

engine-lint:
	cd engine && go vet ./...

# ── Python SDK ──
sdk-python:
	cd sdks/python && uv venv .venv && uv pip install -e ".[dev]"

sdk-python-test:
	cd sdks/python && uv run pytest tests/ -v

sdk-python-lint:
	cd sdks/python && uv run ruff check src/ && uv run mypy src/attest/

# ── Judge replay fixtures ──
# Re-record sdks/python/tests/integration/fixtures/judge/ against a real provider.
# Needs ATTEST_JUDGE_API_KEY (or OPENAI_API_KEY) and ATTEST_JUDGE_MODEL, and refuses without them.
# The committed fixtures were recorded against the scripted judge:
#   cd sdks/python && uv run python -m tests.integration.record_judge_fixtures --scripted
record-judge-fixtures: engine
	cd sdks/python && ATTEST_ENGINE_PATH=$(CURDIR)/bin/attest-engine uv run python -m tests.integration.record_judge_fixtures --real

# ── Security ──
security:
	cd engine && go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
	cd sdks/python && uv pip install pip-audit && uv run pip-audit

# ── All ──
test: verify-versions engine-test sdk-python-test

clean:
	rm -rf bin/ sdks/python/dist/ sdks/python/*.egg-info

# ── Dev setup ──
dev-setup: engine sdk-python
	@echo "Engine built at bin/attest-engine"
	@echo "Python SDK installed in dev mode"
	@echo "Run 'make test' to verify everything works"
