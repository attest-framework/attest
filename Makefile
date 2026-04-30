.PHONY: all engine engine-test engine-lint engine-benchmark sdk-python sdk-python-test sdk-python-lint security protocol-benchmark test clean dev-setup verify-versions

# ── Version source-of-truth ──
verify-versions:
	bash scripts/verify-versions.sh

# ── Engine ──
engine: verify-versions
	cd engine && go build -o ../bin/attest-engine ./cmd/attest-engine/

engine-test:
	cd engine && go test ./... -v -race

engine-lint:
	cd engine && go vet ./...

engine-benchmark:
	cd engine && go test ./internal/benchmark/ -bench=. -benchmem -count=3

# ── Python SDK ──
sdk-python:
	cd sdks/python && uv venv .venv && uv pip install -e ".[dev]"

sdk-python-test:
	cd sdks/python && uv run pytest tests/ -v

sdk-python-lint:
	cd sdks/python && uv run ruff check src/ && uv run mypy src/attest/

# ── Security ──
security:
	cd engine && go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
	cd sdks/python && uv pip install pip-audit && uv run pip-audit

# ── Protocol Benchmark (Phase 0 validation) ──
protocol-benchmark:
	cd engine && go test ./internal/protocol/ -run=^$$ -bench=BenchmarkStdioRoundTrip -benchtime=5s -count=5
	@echo "Compare results against targets in architecture doc"

# ── All ──
test: verify-versions engine-test sdk-python-test

clean:
	rm -rf bin/ sdks/python/dist/ sdks/python/*.egg-info

# ── Dev setup ──
dev-setup: engine sdk-python
	@echo "Engine built at bin/attest-engine"
	@echo "Python SDK installed in dev mode"
	@echo "Run 'make test' to verify everything works"
