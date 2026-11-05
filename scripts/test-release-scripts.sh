#!/usr/bin/env bash
# test-release-scripts.sh — behaviour tests for check-release-tag.sh,
# release-notes.sh and engine-checksums.sh, plus the release.yml wiring that embeds
# and verifies the engine hash table. Runs on every release.yml pull request.
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

if "$here/check-release-tag.sh" $'v1.0.0-rc.1\nversion=9.9.9' "$work/VERSION" 2>"$work/err"; then
    fail "tag containing a newline exits non-zero"
elif grep -q "value not shown" "$work/err" && ! grep -q "9.9.9" "$work/err"; then
    pass "tag containing a newline exits non-zero without echoing it"
else
    fail "tag containing a newline is rejected without echoing it"
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

# engine-checksums.sh -------------------------------------------------------

engines="$work/engines"
mkdir -p "$engines"
for plat in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do printf 'bin %s' "$plat" > "$engines/attest-engine-$plat"; done
for plat in windows-amd64 windows-arm64; do printf 'bin %s' "$plat" > "$engines/attest-engine-$plat.exe"; done
# A checksums file that disagrees with the binaries must be ignored.
echo "0000000000000000000000000000000000000000000000000000000000000000  attest-engine-linux-amd64" > "$engines/checksums-sha256.txt"

linux_sha="$(printf 'bin linux-amd64' | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1)"
win_sha="$(printf 'bin windows-arm64' | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1)"

if "$here/engine-checksums.sh" generate 1.2.3 "$engines" > "$work/table.json" \
    && [[ "$(jq -r '.["1.2.3"]["linux-amd64"]' "$work/table.json")" == "$linux_sha" ]] \
    && [[ "$(jq -r '.["1.2.3"]["windows-arm64"]' "$work/table.json")" == "$win_sha" ]] \
    && [[ "$(jq '.["1.2.3"] | length' "$work/table.json")" == "6" ]]; then
    pass "engine table maps version -> platform -> sha256 of the binaries, not of checksums-sha256.txt"
else
    fail "engine table maps version -> platform -> sha256 of the binaries, not of checksums-sha256.txt"
fi

if "$here/engine-checksums.sh" verify 1.2.3 "$engines" "$work/table.json" 2>/dev/null; then
    pass "table verifies against the binaries it was generated from"
else
    fail "table verifies against the binaries it was generated from"
fi

printf 'tampered' > "$engines/attest-engine-darwin-arm64"
if "$here/engine-checksums.sh" verify 1.2.3 "$engines" "$work/table.json" 2>"$work/err"; then
    fail "verify rejects a modified binary"
elif grep -q 'does not equal the hashes' "$work/err"; then
    pass "verify rejects a modified binary"
else
    fail "verify rejects a modified binary with a clear message"
fi

if "$here/engine-checksums.sh" verify 9.9.9 "$engines" "$work/table.json" 2>/dev/null; then
    fail "verify rejects a table keyed by another version"
else
    pass "verify rejects a table keyed by another version"
fi

rm "$engines/attest-engine-linux-arm64"
if "$here/engine-checksums.sh" generate 1.2.3 "$engines" 2>"$work/err" >/dev/null; then
    fail "generate fails when a platform binary is missing"
elif grep -q 'missing attest-engine-linux-arm64' "$work/err"; then
    pass "generate fails when a platform binary is missing"
else
    fail "generate names the missing binary"
fi

# release.yml wiring -----------------------------------------------------------

workflow="$here/../.github/workflows/release.yml"
job_needs() { awk -v job="  $1:" '$0 == job {f=1; next} f && /^    needs:/ {print; exit} f && /^  [a-z]/ {exit}' "$workflow"; }

for job in build-python build-npm verify-python verify-npm; do
    if job_needs "$job" | grep -q 'build-engine'; then
        pass "$job depends on build-engine"
    else
        fail "$job depends on build-engine"
    fi
done

if grep -Eq 'uses: actions/attest-build-provenance@[0-9a-f]{40} ' "$workflow"; then
    pass "engine provenance attestation is SHA-pinned"
else
    fail "engine provenance attestation is SHA-pinned"
fi

attesting_job="$(awk '/^  [a-z0-9-]+:$/ {job=$1} /attestations: write/ {print job}' "$workflow")"
if [[ "$attesting_job" == "attest-engine:" ]]; then
    pass "attestation permissions are granted to one job only"
else
    fail "attestation permissions are granted to one job only"
fi

# build-engine runs on pull requests and dry runs with a floating toolchain and the binary it
# built; it must never be able to mint an OIDC token.
if awk '/^  [a-z0-9-]+:$/ {job=$1} /id-token: write/ {print job}' "$workflow" | grep -qx 'build-engine:'; then
    fail "build-engine holds no id-token permission"
else
    pass "build-engine holds no id-token permission"
fi

if job_needs attest-engine | grep -q 'build-engine' && job_needs publish-github | grep -q 'attest-engine'; then
    pass "attest-engine follows build-engine and gates publish-github"
else
    fail "attest-engine follows build-engine and gates publish-github"
fi

if awk '$0 == "  attest-engine:" {f=1; next} f && /^  [a-z]/ {exit} f && /^    if:/ {print}' "$workflow" \
    | grep -q "publish == 'true'"; then
    pass "attest-engine runs on publishing runs only"
else
    fail "attest-engine runs on publishing runs only"
fi

unpinned="$(awk '$0 == "  attest-engine:" {f=1; next} f && /^  [a-z]/ {exit} f && /uses:/ && !/@[0-9a-f]{40} / {print}' "$workflow")"
if [[ -z "$unpinned" ]]; then
    pass "every attest-engine step is SHA-pinned"
else
    fail "every attest-engine step is SHA-pinned: $unpinned"
fi

if [[ $failures -gt 0 ]]; then
    echo "$failures release-script test(s) failed" >&2
    exit 1
fi
echo "all release-script tests passed"
