#!/usr/bin/env bash
# Mirror the two public, pinned Mac host inputs without changing their bytes.
set -euo pipefail
cd "$(dirname "$0")/.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
if [ "$#" -ne 1 ]; then
    echo 'Mac runtime mirror: one release version is required.' >&2
    echo 'Try: scripts/mirror-mac-runtime.sh <release>' >&2
    exit 1
fi
go run -tags="$go_build_tags" ./cmd/supply mirror-mac-runtime -release "$1" -dir build/mac-runtime -lock guest/mac-host.lock
