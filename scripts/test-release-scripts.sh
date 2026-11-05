#!/usr/bin/env bash
# test-release-scripts.sh — behaviour tests for check-release-tag.sh, release-notes.sh,
# engine-checksums.sh and npm-publish-dry-run.sh, plus the release workflow layout and the engine hash
# table wiring. Exits non-zero on the first failed case.
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

# release workflow wiring -----------------------------------------------------

root="$(cd "$here/.." && pwd)"
wf="$root/.github/workflows"
build_wf="$wf/release-build.yml"
workflow="$wf/release.yml"
job_needs() { # <file> <job>
    awk -v job="  $2:" '$0 == job {f=1; next} f && /^    needs:/ {print; exit} f && /^  [a-z]/ {exit}' "$1"
}

for job in build-python build-npm verify-python verify-npm; do
    if job_needs "$build_wf" "$job" | grep -q 'build-engine'; then
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

# release-build.yml runs on pull requests and dry runs with a floating toolchain and the binary it
# built; no job in it may be able to mint an OIDC token or write attestations.
if grep -Eq '^\s+(id-token|attestations): write' "$build_wf" "$wf/ci.yml"; then
    fail "release-build.yml and ci.yml hold no id-token or attestations permission"
else
    pass "release-build.yml and ci.yml hold no id-token or attestations permission"
fi

if job_needs "$workflow" attest-engine | grep -q 'build' && job_needs "$workflow" publish-github | grep -q 'attest-engine'; then
    pass "attest-engine follows the build and gates publish-github"
else
    fail "attest-engine follows the build and gates publish-github"
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

# npm-publish-dry-run.sh ----------------------------------------------------

# A stand-in `npm` on PATH: `view` answers per NPM_VIEW (published, absent,
# broken); `publish` is recorded in $work/npm-calls.
mkdir -p "$work/bin" "$work/dist"
cat > "$work/bin/npm" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$NPM_CALLS"
case "$1" in
    view)
        case "$NPM_VIEW" in
            published) echo "${2##*@}" ;;
            absent) echo "npm error code E404" >&2; exit 1 ;;
            *) echo "npm error code EAI_AGAIN" >&2; exit 1 ;;
        esac
        ;;
    publish) ;;
    *) exit 2 ;;
esac
STUB
chmod +x "$work/bin/npm"
for pkg in core vitest; do
    mkdir -p "$work/pack/package"
    printf '{"name":"@attest-ai/%s","version":"1.0.1"}\n' "$pkg" > "$work/pack/package/package.json"
    tar -czf "$work/dist/attest-ai-$pkg-1.0.1.tgz" -C "$work/pack" package
done

dry() { # <mode> <NPM_VIEW> [version]
    : > "$work/npm-calls"
    PATH="$work/bin:$PATH" NPM_CALLS="$work/npm-calls" NPM_VIEW="$2" \
        "$here/npm-publish-dry-run.sh" "$1" "${3:-1.0.1}" latest "$work/dist"
}

if dry dry-run published 2>"$work/err" >"$work/out" \
    && grep -q 'skipping npm publish --dry-run' "$work/out" \
    && ! grep -q '^publish' "$work/npm-calls"; then
    pass "dry-run skips npm publish --dry-run for a version already on npm"
else
    fail "dry-run skips npm publish --dry-run for a version already on npm"
fi

if dry dry-run absent >/dev/null 2>&1 \
    && [[ "$(grep -c '^publish .*--dry-run' "$work/npm-calls")" == 2 ]]; then
    pass "dry-run runs npm publish --dry-run for both packages when the version is new"
else
    fail "dry-run runs npm publish --dry-run for both packages when the version is new"
fi

if dry publish published >/dev/null 2>&1 \
    && [[ "$(grep -c '^publish .*--dry-run' "$work/npm-calls")" == 2 ]] \
    && ! grep -q '^view' "$work/npm-calls"; then
    pass "publish mode never consults the registry and always runs the dry run"
else
    fail "publish mode never consults the registry and always runs the dry run"
fi

if dry dry-run broken >/dev/null 2>"$work/err"; then
    fail "dry-run fails on a registry lookup error other than E404"
elif grep -q 'EAI_AGAIN' "$work/err"; then
    pass "dry-run fails on a registry lookup error other than E404"
else
    fail "dry-run reports the registry lookup error"
fi

# A 1.0.2-named tarball that holds 1.0.1: the name/version check must reject it.
for pkg in core vitest; do
    cp "$work/dist/attest-ai-$pkg-1.0.1.tgz" "$work/dist/attest-ai-$pkg-1.0.2.tgz"
done
if dry dry-run published 1.0.2 >/dev/null 2>"$work/err"; then
    fail "a tarball whose version differs from the expected one is rejected"
elif grep -q "holds '@attest-ai/core@1.0.1', expected '@attest-ai/core@1.0.2'" "$work/err"; then
    pass "a tarball whose version differs from the expected one is rejected"
else
    fail "a tarball whose version differs is rejected by the name/version check"
fi
rm -f "$work/dist/"attest-ai-*-1.0.2.tgz

# release-build.yml passes a relative dist dir; npm must get ./dist/..., which it reads as a file.
: > "$work/npm-calls"
if (cd "$work" && PATH="$work/bin:$PATH" NPM_CALLS="$work/npm-calls" NPM_VIEW=absent \
    "$here/npm-publish-dry-run.sh" dry-run 1.0.1 latest dist >/dev/null 2>&1) \
    && grep -q '^publish \./dist/attest-ai-core-1\.0\.1\.tgz --dry-run' "$work/npm-calls" \
    && ! grep -q '^publish dist/' "$work/npm-calls"; then
    pass "a relative dist dir reaches npm as ./dist/..., not a GitHub spec"
else
    fail "a relative dist dir reaches npm as ./dist/..., not a GitHub spec"
fi

# Workflow layout ------------------------------------------------------------

if grep -Eq '^permissions:$' "$wf/release-build.yml" \
    && [[ "$(awk '/^permissions:/{getline; print}' "$wf/release-build.yml")" == "  contents: read" ]] \
    && ! grep -Eq '^\s+(contents: write|id-token: write)' "$wf/release-build.yml"; then
    pass "release-build.yml is read-only"
else
    fail "release-build.yml is read-only"
fi

missing=""
for job in check-tag release-scripts-test build-engine build-python build-npm \
    verify-engine verify-python verify-npm; do
    grep -Eq "^  $job:\$" "$wf/release-build.yml" || missing="$missing $job(build)"
    ! grep -Eq "^  $job:\$" "$wf/release.yml" || missing="$missing $job(release)"
done
for job in publish-github publish-pypi publish-npm finalize-release; do
    grep -Eq "^  $job:\$" "$wf/release.yml" || missing="$missing $job(release)"
done
if [[ -z "$missing" ]]; then
    pass "build and verify jobs live in release-build.yml, publish jobs in release.yml"
else
    fail "job layout wrong:$missing"
fi

if grep -Eq '^\s+uses: \./\.github/workflows/release-build\.yml$' "$wf/release.yml" \
    && grep -Eq '^\s+uses: \./\.github/workflows/release-build\.yml$' "$wf/ci.yml" \
    && ! grep -Eq '^\s+uses: \./\.github/workflows/release\.yml$' "$wf/ci.yml"; then
    pass "release.yml and ci.yml both call release-build.yml; ci.yml never calls release.yml"
else
    fail "release.yml and ci.yml both call release-build.yml; ci.yml never calls release.yml"
fi

if awk '/^  release-dry-run:/{f=1;next} f&&/^  [a-z]/{f=0} f' "$wf/ci.yml" \
    | grep -q "github.event_name == 'pull_request'"; then
    pass "ci.yml calls the release dry run only on pull_request"
else
    fail "ci.yml calls the release dry run only on pull_request"
fi

if [[ $failures -gt 0 ]]; then
    echo "$failures release-script test(s) failed" >&2
    exit 1
fi
echo "all release-script tests passed"
