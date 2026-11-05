#!/usr/bin/env bash
# test-release-scripts.sh — behaviour tests for check-release-tag.sh,
# release-notes.sh and npm-publish-dry-run.sh, plus the release workflow layout. Exits non-zero on the first failed case.
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

if dry dry-run published 1.0.2 >/dev/null 2>"$work/err"; then
    fail "a tarball whose version differs from the expected one is rejected"
else
    pass "a tarball whose version differs from the expected one is rejected"
fi

# Workflow layout ------------------------------------------------------------

root="$(cd "$here/.." && pwd)"
wf="$root/.github/workflows"

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
