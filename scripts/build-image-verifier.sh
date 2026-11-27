#!/usr/bin/env bash
# Build the offline image signature verifier from its pinned module using the
# repository's pinned Go toolchain (go.mod).
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -ne 1 ] || [ -z "$1" ]; then
    echo 'image-verifier: release version argument is required. Try: run `just image-verifiers` or pass a version string.' >&2
    exit 1
fi
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
expected_go=$(sed -n 's/^toolchain //p' tools/imageverify/go.mod)
if [ -z "$expected_go" ]; then
    expected_go=$(sed -n 's/^toolchain //p' go.mod)
fi
actual_go=$(go version | awk '{print $3}')
if [ -z "$expected_go" ] || [ "$actual_go" != "$expected_go" ]; then
    echo "image-verifier: Go toolchain is $actual_go, want $expected_go. Try: unset GOTOOLCHAIN so Go downloads the pinned toolchain." >&2
    exit 1
fi
verifier_version=$1
root=$(pwd -P)
mkdir -p internal/client/helpers
for system in darwin linux; do
    for arch in amd64 arm64; do
        (cd tools/imageverify && CGO_ENABLED=0 GOOS="$system" GOARCH="$arch" \
            go build -tags=http2legacy -mod=readonly -trimpath \
            -ldflags "-s -w -X main.Version=$verifier_version" \
            -o "$root/internal/client/helpers/image-verifier-$system-$arch" .)
    done
done
