#!/usr/bin/env bash
# test-release-scripts.sh — behaviour tests for check-release-tag.sh and
# release-notes.sh. Exits non-zero on the first failed case.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; failures=$((failures + 1)); }

# check-release-tag.sh ------------------------------------------------------

echo "1.0.0-rc.1" > "$work/VERSION"

if out="$("$here/check-release-tag.sh" v1.0.0-rc.1 "$work/VERSION")" \
    && grep -qx 'version=1.0.0-rc.1' <<<"$out" \
    && grep -qx 'prerelease=true' <<<"$out" \
    && grep -qx 'npm_dist_tag=next' <<<"$out"; then
    pass "matching rc tag passes and reports prerelease"
else
    fail "matching rc tag passes and reports prerelease"
fi

if "$here/check-release-tag.sh" v1.0.0-rc.2 "$work/VERSION" 2>"$work/err"; then
    fail "mismatched tag exits non-zero"
elif grep -q "does not match VERSION (expected 'v1.0.0-rc.1')" "$work/err"; then
    pass "mismatched tag exits non-zero: $(cat "$work/err")"
else
    fail "mismatched tag reports the expected tag"
fi

if "$here/check-release-tag.sh" 1.0.0-rc.1 "$work/VERSION" 2>/dev/null; then
    fail "tag without leading v exits non-zero"
else
    pass "tag without leading v exits non-zero"
fi

echo "1.0.0" > "$work/VERSION"
if out="$("$here/check-release-tag.sh" v1.0.0 "$work/VERSION")" \
    && grep -qx 'prerelease=false' <<<"$out" \
    && grep -qx 'npm_dist_tag=latest' <<<"$out"; then
    pass "final release is not a prerelease"
else
    fail "final release is not a prerelease"
fi

echo "not-a-version" > "$work/VERSION"
if "$here/check-release-tag.sh" vnot-a-version "$work/VERSION" 2>/dev/null; then
    fail "non-SemVer VERSION exits non-zero"
else
    pass "non-SemVer VERSION exits non-zero"
fi

# release-notes.sh ----------------------------------------------------------

cat > "$work/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Added

- Pending feature.

## [1.0.0-rc.1] — 2026-11-02

### Fixed

- A fix.

## [1.0.0-rc] — 2026-04-30

- Older entry.

## [0.9.0] — 2026-04-30

## [0.8.0] — 2026-04-30

- Last entry.

## [0.7.0] — 2026-04-29

- See [the spec][1].

[1]: https://example.invalid/spec

[Unreleased]: https://example.invalid/compare
EOF

if [[ "$("$here/release-notes.sh" Unreleased "$work/CHANGELOG.md")" == $'### Added\n\n- Pending feature.' ]]; then
    pass "Unreleased section extracted without surrounding blank lines"
else
    fail "Unreleased section extracted without surrounding blank lines"
fi

if [[ "$("$here/release-notes.sh" 1.0.0-rc "$work/CHANGELOG.md")" == "- Older entry." ]]; then
    pass "1.0.0-rc does not match the 1.0.0-rc.1 heading"
else
    fail "1.0.0-rc does not match the 1.0.0-rc.1 heading"
fi

if [[ "$("$here/release-notes.sh" 0.8.0 "$work/CHANGELOG.md")" == "- Last entry." ]]; then
    pass "last section stops at the link-reference block"
else
    fail "last section stops at the link-reference block"
fi

if [[ "$("$here/release-notes.sh" 0.7.0 "$work/CHANGELOG.md")" == $'- See [the spec][1].\n\n[1]: https://example.invalid/spec' ]]; then
    pass "in-section link reference is kept"
else
    fail "in-section link reference is kept"
fi

if "$here/release-notes.sh" 0.9.0 "$work/CHANGELOG.md" 2>/dev/null; then
    fail "empty section exits non-zero"
else
    pass "empty section exits non-zero"
fi

if "$here/release-notes.sh" 9.9.9 "$work/CHANGELOG.md" 2>/dev/null; then
    fail "missing section exits non-zero"
else
    pass "missing section exits non-zero"
fi

if [[ $failures -gt 0 ]]; then
    echo "$failures release-script test(s) failed" >&2
    exit 1
fi
echo "all release-script tests passed"
