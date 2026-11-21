#!/usr/bin/env bash
# A shell-quoted prepared image path can vary between real repro passes.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -ne 1 ]; then
    echo 'release manifest: one release version is required. Try: use the existing GoReleaser build.' >&2
    exit 1
fi
go run ./cmd/supply write -release "$1" -guest build/guest -require-guest -require-kernel-efi \
    -vmm build/vmm -require-vmm \
    -gvisor "${GVISOR_MANIFEST_INPUTS:-build/gvisor}" -require-gvisor \
    -mac-runtime build/mac-runtime -require-mac-runtime \
    -images "${DEFAULT_IMAGE_MANIFEST_INPUTS:-build/default-image}" -require-images \
    -require-launch -require-image-verifier internal/client/helpers
