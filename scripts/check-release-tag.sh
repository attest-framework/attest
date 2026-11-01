#!/usr/bin/env bash
# check-release-tag.sh — assert a release tag names the canonical VERSION.
#
# Usage: scripts/check-release-tag.sh <tag> [version-file]
#
# Compares <tag> with "v$(cat VERSION)". On a match, prints key=value lines
# suitable for $GITHUB_OUTPUT:
#
#   version=1.0.0-rc.1
#   tag=v1.0.0-rc.1
#   prerelease=true         (any SemVer pre-release suffix)
#   npm_dist_tag=next       (latest for final releases)
#
# Exits 1 with a diagnostic on stderr when the tag differs from VERSION or
# VERSION is not a SemVer string.
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
    echo "usage: $0 <tag> [version-file]" >&2
    exit 2
fi

tag="$1"
if [[ ! "$tag" =~ ^v[0-9A-Za-z.+-]+$ ]]; then
    echo "check-release-tag: tag must match ^v[0-9A-Za-z.+-]+\$ (value not shown)" >&2
    exit 1
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version_file="${2:-$repo_root/VERSION}"

if [[ ! -f "$version_file" ]]; then
    echo "check-release-tag: VERSION file not found at $version_file" >&2
    exit 1
fi

version="$(tr -d '[:space:]' < "$version_file")"
semver_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
if [[ ! "$version" =~ $semver_re ]]; then
    echo "check-release-tag: VERSION '$version' is not a SemVer string" >&2
    exit 1
fi

expected="v$version"
if [[ "$tag" != "$expected" ]]; then
    echo "check-release-tag: tag '$tag' does not match VERSION (expected '$expected')" >&2
    exit 1
fi

if [[ "$version" == *-* ]]; then
    prerelease=true
    npm_dist_tag=next
else
    prerelease=false
    npm_dist_tag=latest
fi

echo "version=$version"
echo "tag=$tag"
echo "prerelease=$prerelease"
echo "npm_dist_tag=$npm_dist_tag"
