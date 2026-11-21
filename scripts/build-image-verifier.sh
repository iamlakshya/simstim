#!/usr/bin/env bash
# Build only the isolated offline verifier. Main Go/toolchain/PATH stay fixed.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -ne 1 ] || [ -z "${IMAGEVERIFY_GO:-}" ] || [ -z "${IMAGEVERIFY_GO_SHA256:-}" ] ||
    [ -z "${IMAGEVERIFY_GOMODCACHE:-}" ] || [ -z "${IMAGEVERIFY_GOCACHE:-}" ] || [ -z "${IMAGEVERIFY_HOME:-}" ]; then
    echo 'image-verifier: separately verified Go 1.27.4 is required. Try: prepare its official checksum-bound executable through the approved build runner.' >&2
    exit 1
fi
case "$IMAGEVERIFY_GO" in /*) ;; *) echo 'image-verifier: toolchain path must be absolute. Try: use the approved prepared compiler.' >&2; exit 1 ;; esac
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$IMAGEVERIFY_GO"); actual=${actual%% *}
else
    actual=$(shasum -a 256 "$IMAGEVERIFY_GO"); actual=${actual%% *}
fi
if [ "$actual" != "$IMAGEVERIFY_GO_SHA256" ] || [ "$("$IMAGEVERIFY_GO" version | awk '{print $3}')" != go1.27.4 ]; then
    echo 'image-verifier: compiler identity differs from the prepared official toolchain. Try: preserve the main toolchain and prepare the isolated exact version.' >&2
    exit 1
fi
verifier_version=$1
root=$(pwd -P)
mkdir -p internal/client/helpers
for system in darwin linux; do
    for arch in amd64 arm64; do
        (cd tools/imageverify && CGO_ENABLED=0 GOOS="$system" GOARCH="$arch" \
            HOME="$IMAGEVERIFY_HOME" USERPROFILE="$IMAGEVERIFY_HOME" \
            XDG_CONFIG_HOME="$IMAGEVERIFY_HOME/config" XDG_CACHE_HOME="$IMAGEVERIFY_HOME/cache" \
            XDG_DATA_HOME="$IMAGEVERIFY_HOME/data" XDG_STATE_HOME="$IMAGEVERIFY_HOME/state" \
            XDG_RUNTIME_DIR="$IMAGEVERIFY_HOME/run" XDG_CONFIG_DIRS="$IMAGEVERIFY_HOME/config-dirs" \
            XDG_DATA_DIRS="$IMAGEVERIFY_HOME/data-dirs" GOMODCACHE="$IMAGEVERIFY_GOMODCACHE" GOCACHE="$IMAGEVERIFY_GOCACHE" \
            GOTOOLCHAIN=local GOPROXY=off "$IMAGEVERIFY_GO" build -tags=http2legacy -mod=readonly -trimpath \
            -ldflags "-s -w -X main.Version=$verifier_version" \
            -o "$root/internal/client/helpers/image-verifier-$system-$arch" .)
    done
done
