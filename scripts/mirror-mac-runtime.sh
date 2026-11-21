#!/usr/bin/env bash
# Mirror the two public, pinned Mac host inputs without changing their bytes.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -ne 1 ]; then
    echo 'Mac runtime mirror: one release version is required.' >&2
    echo 'Try: scripts/mirror-mac-runtime.sh <release>' >&2
    exit 1
fi
go run ./cmd/supply mirror-mac-runtime -release "$1" -dir build/mac-runtime -lock guest/mac-host.lock
