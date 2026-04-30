#!/usr/bin/env bash
# verify-versions.sh — assert every language artefact agrees with VERSION.
#
# Reads the canonical version from the repo-root VERSION file, then
# compares against:
#
#   - engine/internal/buildinfo/VERSION   (mirror used by go:embed)
#   - sdks/python/pyproject.toml          (project.version)
#   - sdks/python/src/attest/__init__.py  (__version__, ENGINE_VERSION)
#   - sdks/typescript/packages/core/package.json         (.version)
#   - sdks/typescript/packages/core/src/version.ts       (VERSION, ENGINE_VERSION)
#   - sdks/typescript/packages/vitest/package.json       (.version)
#
# Exits 0 on agreement, 1 on any drift, with a per-source diff report.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if [[ ! -f VERSION ]]; then
    echo "verify-versions: VERSION file missing at repo root" >&2
    exit 1
fi

canonical="$(tr -d '[:space:]' < VERSION)"
if [[ -z "$canonical" ]]; then
    echo "verify-versions: VERSION file is empty" >&2
    exit 1
fi

declare -a mismatches=()

check() {
    local source="$1"
    local actual="$2"
    if [[ "$actual" != "$canonical" ]]; then
        mismatches+=("$source: $actual (expected $canonical)")
    fi
}

# Engine //go:embed mirror.
embed_version="$(tr -d '[:space:]' < engine/internal/buildinfo/VERSION)"
check "engine/internal/buildinfo/VERSION" "$embed_version"

# Python pyproject.toml — project.version is required and unique per file.
py_pyproject="$(grep -E '^version[[:space:]]*=' sdks/python/pyproject.toml \
    | head -1 | sed -E 's/^version[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/')"
check "sdks/python/pyproject.toml::project.version" "$py_pyproject"

# Python __init__.py — both __version__ and ENGINE_VERSION.
py_init_version="$(grep -E '^__version__:' sdks/python/src/attest/__init__.py \
    | sed -E 's/^.*"([^"]+)".*/\1/')"
check "sdks/python/src/attest/__init__.py::__version__" "$py_init_version"

py_init_engine="$(grep -E '^ENGINE_VERSION:' sdks/python/src/attest/__init__.py \
    | sed -E 's/^.*"([^"]+)".*/\1/')"
check "sdks/python/src/attest/__init__.py::ENGINE_VERSION" "$py_init_engine"

# TypeScript core package.json — top-level "version" field (jq if available).
ts_core_pkg="$(node -e \
    'process.stdout.write(require("./sdks/typescript/packages/core/package.json").version)')"
check "sdks/typescript/packages/core/package.json::version" "$ts_core_pkg"

# TypeScript version.ts — both VERSION and ENGINE_VERSION constants.
ts_core_version="$(grep -E '^export const VERSION' \
    sdks/typescript/packages/core/src/version.ts \
    | sed -E 's/^.*"([^"]+)".*/\1/')"
check "sdks/typescript/packages/core/src/version.ts::VERSION" "$ts_core_version"

ts_core_engine="$(grep -E '^export const ENGINE_VERSION' \
    sdks/typescript/packages/core/src/version.ts \
    | sed -E 's/^.*"([^"]+)".*/\1/')"
check "sdks/typescript/packages/core/src/version.ts::ENGINE_VERSION" "$ts_core_engine"

# TypeScript vitest package.json.
ts_vitest_pkg="$(node -e \
    'process.stdout.write(require("./sdks/typescript/packages/vitest/package.json").version)')"
check "sdks/typescript/packages/vitest/package.json::version" "$ts_vitest_pkg"

if (( ${#mismatches[@]} > 0 )); then
    echo "verify-versions: ${#mismatches[@]} source(s) drifted from VERSION=$canonical:" >&2
    for line in "${mismatches[@]}"; do
        echo "  - $line" >&2
    done
    exit 1
fi

echo "verify-versions: all sources agree on $canonical"
