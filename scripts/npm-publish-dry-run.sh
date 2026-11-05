#!/usr/bin/env bash
# npm-publish-dry-run.sh — verify the packed @attest-ai tarballs with
# `npm publish --dry-run`.
#
# Usage: npm-publish-dry-run.sh <publish|dry-run> <version> <dist-tag> <dist-dir>
#
# Every tarball is checked for the expected package name and version. In
# `publish` mode (the run that will publish) the dry run always executes, so a
# version already on the registry fails here as it does at publish time. In
# `dry-run` mode (pull requests, dispatch without publishing) a version the
# registry already holds cannot be published over, so the dry run is skipped
# with a notice instead of failing every pull request after a release.
set -euo pipefail

if [[ $# -ne 4 ]]; then
    echo "usage: ${0##*/} <publish|dry-run> <version> <dist-tag> <dist-dir>" >&2
    exit 2
fi
mode="$1"
version="$2"
dist_tag="$3"
dist_dir="$4"
case "$mode" in
    publish | dry-run) ;;
    *)
        echo "::error::mode must be publish or dry-run, got '$mode'" >&2
        exit 2
        ;;
esac

err="$(mktemp)"
trap 'rm -f "$err"' EXIT

for pkg in core vitest; do
    name="@attest-ai/$pkg"
    tgz="$dist_dir/attest-ai-$pkg-$version.tgz"

    packed="$(tar -xOzf "$tgz" package/package.json | jq -r '.name + "@" + .version')"
    if [[ "$packed" != "$name@$version" ]]; then
        echo "::error::$tgz holds '$packed', expected '$name@$version'" >&2
        exit 1
    fi

    if [[ "$mode" == "dry-run" ]]; then
        if published="$(npm view "$name@$version" version 2> "$err")"; then
            if [[ "$published" == "$version" ]]; then
                echo "::notice::$name@$version is already on npm; skipping npm publish --dry-run (it cannot publish over an existing version)"
                continue
            fi
        elif ! grep -q 'E404' "$err"; then
            echo "::error::npm lookup for $name@$version failed:" >&2
            cat "$err" >&2
            exit 1
        fi
    fi

    npm publish "$tgz" --dry-run --access public --tag "$dist_tag"
done
