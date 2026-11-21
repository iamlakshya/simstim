#!/usr/bin/env bash
# A shell-quoted prepared image path can vary between real repro passes.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -ne 1 ]; then
    echo 'release manifest: one release version is required. Try: use the existing GoReleaser build.' >&2
    exit 1
fi
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
go run -tags="$go_build_tags" ./cmd/supply write -release "$1" -guest build/guest -require-guest -require-kernel-efi \
    -vmm build/vmm -require-vmm \
    -gvisor "${GVISOR_MANIFEST_INPUTS:-build/gvisor}" -require-gvisor \
    -mac-runtime build/mac-runtime -require-mac-runtime \
    -images "${DEFAULT_IMAGE_MANIFEST_INPUTS:-build/default-image}" -require-images \
    -require-launch -require-image-verifier internal/client/helpers
