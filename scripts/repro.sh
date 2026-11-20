#!/usr/bin/env bash
# Builds every release binary twice, each time from empty Go and Zig caches,
# and fails if any binary differs. Needs goreleaser and Zig 0.17.
set -euo pipefail
cd "$(dirname "$0")/.."

APP_NAME=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
MODULE=$(sed -n 's/^module //p' go.mod)
export APP_NAME MODULE

out=$(mktemp -d)
trap 'chmod -R u+w "$out" 2>/dev/null; rm -rf "$out"' EXIT
for run in 1 2; do
    rm -rf vt/.zig-cache vt/zig-out
    GOCACHE="$out/gocache-$run" ZIG_GLOBAL_CACHE_DIR="$out/zigcache-$run" \
        goreleaser build --snapshot --clean >"$out/build-$run.log" 2>&1 ||
        { cat "$out/build-$run.log" >&2; exit 1; }
    find dist -type f -perm -u+x ! -name '*.json' -exec shasum -a 256 {} + | sort -k2 >"$out/$run.txt"
done
if ! diff "$out/1.txt" "$out/2.txt"; then
    echo "repro: the two builds differ" >&2
    exit 1
fi
echo "repro: ok, $(wc -l <"$out/1.txt" | tr -d ' ') binaries are byte-identical across two builds from empty caches"
