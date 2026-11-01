#!/usr/bin/env bash
# release-notes.sh — print one version's section from CHANGELOG.md.
#
# Usage: scripts/release-notes.sh <version|Unreleased> [changelog]
#
# Prints the body under "## [<version>]" up to the next "## [" heading or
# the Keep a Changelog link-reference block, with surrounding blank lines
# trimmed. Exits 1 when the section is missing or empty.
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
    echo "usage: $0 <version|Unreleased> [changelog]" >&2
    exit 2
fi

version="$1"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
changelog="${2:-$repo_root/CHANGELOG.md}"

if [[ ! -f "$changelog" ]]; then
    echo "release-notes: changelog not found at $changelog" >&2
    exit 1
fi

notes="$(
    awk -v want="## [$version]" '
        index($0, want) == 1 { found = 1; next }
        found && (/^## \[/ || /^\[[^]]+\]: /) { exit }
        found { print }
    ' "$changelog" | sed -e '/./,$!d'
)"

if [[ -z "${notes//[[:space:]]/}" ]]; then
    echo "release-notes: no '## [$version]' section with content in $changelog" >&2
    exit 1
fi

printf '%s\n' "$notes"
