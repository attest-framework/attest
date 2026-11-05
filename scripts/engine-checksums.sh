#!/usr/bin/env bash
# engine-checksums.sh — build and verify engine-checksums.json, the table the SDK
# downloaders trust (version -> platform -> sha256).
#
#   engine-checksums.sh generate <version> <engine-dir>             table on stdout
#   engine-checksums.sh verify   <version> <engine-dir> <table.json>
#
# <engine-dir> holds the six attest-engine-<os>-<arch>[.exe] binaries. The hashes are
# computed from the binaries themselves, never read from checksums-sha256.txt.
set -euo pipefail

platforms=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64)

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        shasum -a 256 "$1" | cut -d' ' -f1
    fi
}

asset_for() {
    local plat="$1"
    if [[ "$plat" == windows-* ]]; then echo "attest-engine-$plat.exe"; else echo "attest-engine-$plat"; fi
}

generate() {
    local version="$1" dir="$2" plat asset file entries=()
    for plat in "${platforms[@]}"; do
        asset="$(asset_for "$plat")"
        file="$dir/$asset"
        if [[ ! -f "$file" ]]; then
            echo "engine-checksums: missing $asset in $dir" >&2
            return 1
        fi
        entries+=("$plat" "$(sha256_of "$file")")
    done
    jq -n --arg v "$version" '
        reduce range(0; ($ARGS.positional | length); 2) as $i
            ({}; .[$ARGS.positional[$i]] = $ARGS.positional[$i + 1])
        | {($v): .}' --args "${entries[@]}"
}

verify() {
    local version="$1" dir="$2" table="$3" expected actual
    if [[ ! -f "$table" ]]; then
        echo "engine-checksums: $table does not exist" >&2
        return 1
    fi
    expected="$(jq -S . "$table")"
    actual="$(generate "$version" "$dir" | jq -S .)"
    if [[ "$expected" != "$actual" ]]; then
        echo "engine-checksums: $table does not equal the hashes of the binaries in $dir" >&2
        diff <(echo "$expected") <(echo "$actual") >&2 || true
        return 1
    fi
}

case "${1:-}" in
    generate) [[ $# -eq 3 ]] || { echo "usage: $0 generate <version> <engine-dir>" >&2; exit 2; }; generate "$2" "$3" ;;
    verify)   [[ $# -eq 4 ]] || { echo "usage: $0 verify <version> <engine-dir> <table.json>" >&2; exit 2; }; verify "$2" "$3" "$4" ;;
    *) echo "usage: $0 generate|verify ..." >&2; exit 2 ;;
esac
