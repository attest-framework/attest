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

# Extract the first double-quoted value from the first line in `file`
# matching the regex `pattern`. Used to read version literals out of
# Python and TypeScript source where there's exactly one quoted token
# per matching line (e.g. `version = "0.6.1"` or `export const VERSION = "0.6.1";`).
extract_quoted() {
    local file="$1"
    local pattern="$2"
    grep -E "$pattern" "$file" | head -1 | sed -E 's/^.*"([^"]+)".*/\1/'
}

# Read the `.version` field of a JSON package manifest using Node, which
# is already required by the TypeScript SDK toolchain.
read_pkg_version() {
    node -e "process.stdout.write(require('./$1').version)"
}

# Engine //go:embed mirror.
embed_version="$(tr -d '[:space:]' < engine/internal/buildinfo/VERSION)"
check "engine/internal/buildinfo/VERSION" "$embed_version"

# Python pyproject.toml — project.version is the first `version = "…"` line.
check "sdks/python/pyproject.toml::project.version" \
    "$(extract_quoted sdks/python/pyproject.toml '^version[[:space:]]*=')"

# Python __init__.py — both __version__ and ENGINE_VERSION.
check "sdks/python/src/attest/__init__.py::__version__" \
    "$(extract_quoted sdks/python/src/attest/__init__.py '^__version__:')"
check "sdks/python/src/attest/__init__.py::ENGINE_VERSION" \
    "$(extract_quoted sdks/python/src/attest/__init__.py '^ENGINE_VERSION:')"

# TypeScript core package.json + version.ts (VERSION and ENGINE_VERSION).
check "sdks/typescript/packages/core/package.json::version" \
    "$(read_pkg_version sdks/typescript/packages/core/package.json)"
check "sdks/typescript/packages/core/src/version.ts::VERSION" \
    "$(extract_quoted sdks/typescript/packages/core/src/version.ts '^export const VERSION')"
check "sdks/typescript/packages/core/src/version.ts::ENGINE_VERSION" \
    "$(extract_quoted sdks/typescript/packages/core/src/version.ts '^export const ENGINE_VERSION')"

# TypeScript vitest package.json.
check "sdks/typescript/packages/vitest/package.json::version" \
    "$(read_pkg_version sdks/typescript/packages/vitest/package.json)"

if (( ${#mismatches[@]} > 0 )); then
    echo "verify-versions: ${#mismatches[@]} source(s) drifted from VERSION=$canonical:" >&2
    for line in "${mismatches[@]}"; do
        echo "  - $line" >&2
    done
    exit 1
fi

echo "verify-versions: all sources agree on $canonical"
