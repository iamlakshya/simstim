#!/usr/bin/env bash
# Two real builds from empty caches, with independently prepared native image
# pairs as explicit inputs.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
if [ "$#" -ne 0 ] || [ -z "${DEFAULT_IMAGE_REPRO_AMD64:-}" ] ||
    [ -z "${DEFAULT_IMAGE_REPRO_ARM64:-}" ] || [ -z "${GVISOR_REPRO_AMD64:-}" ] ||
    [ -z "${GVISOR_REPRO_ARM64:-}" ]; then
    echo 'repro: native default image pairs and native gVisor pairs are required. Try: follow docs/contributing/default-image-build.md before running just repro.' >&2
    exit 1
fi
for input in "$DEFAULT_IMAGE_REPRO_AMD64" "$DEFAULT_IMAGE_REPRO_ARM64" "$GVISOR_REPRO_AMD64" "$GVISOR_REPRO_ARM64"; do
    case "$input" in /*) ;; *)
        echo 'repro: prepared inputs must have absolute paths. Try: pass absolute paths for the prepared artifact directories.' >&2
        exit 1 ;; esac
done
# Go runs the toolchain that go.mod pins, and fetches and checks it when the
# local Go differs. go mod download checks every module of both modules
# against go.sum and the checksum database; both passes then read that one
# module cache offline.
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go mod download
(cd tools/imageverify && go mod download)
GOMODCACHE=$(go env GOMODCACHE)
APP_NAME=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
MODULE=$(sed -n 's/^module //p' go.mod)
export APP_NAME MODULE GOMODCACHE GOPROXY=off
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
out=$(mktemp -d "${TMPDIR:-/tmp}/release-repro.XXXXXXXX")
# Retain both actual passes and their source/input/hash evidence on failure
# and success. Never erase a prior native image preparation.
shasum -a 256 go.mod go.sum tools/imageverify/go.mod tools/imageverify/go.sum \
    guest/supply.lock internal/image/default_image.toml \
    internal/supply/trusted_root.json >"$out/source-pins-before.txt"
shasum -a 256 go.mod go.sum guest/supply.lock internal/image/default_image.toml \
    guest/base-image/build.sh guest/base-image/prepare.sh >"$out/native-image-source-pins.txt"
shasum -a 256 third_party/gvisor/build-tools.json third_party/gvisor/hostguard.patch \
    third_party/gvisor/hostguard-inputs.json internal/wall/gvisor/native_build.go \
    scripts/prepare-gvisor-build.py scripts/build-gvisor-runtime.py >"$out/gvisor-source-pins.txt"
for run in 1 2; do
    state="$out/run-$run"
    mkdir -p "$state/default-image" "$state/gvisor" "$state/home"
    export HOME="$state/home" USERPROFILE="$state/home"
    export XDG_CONFIG_HOME="$state/xdg/config" XDG_CACHE_HOME="$state/xdg/cache"
    export XDG_DATA_HOME="$state/xdg/data" XDG_STATE_HOME="$state/xdg/state"
    export XDG_RUNTIME_DIR="$state/xdg/run" XDG_CONFIG_DIRS="$state/xdg/config-dirs"
    export XDG_DATA_DIRS="$state/xdg/data-dirs" GOCACHE="$state/go-build"
    mkdir -p "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" \
        "$XDG_RUNTIME_DIR" "$XDG_CONFIG_DIRS" "$XDG_DATA_DIRS" "$GOCACHE"
    for arch in amd64 arm64; do
        native_pair=$DEFAULT_IMAGE_REPRO_AMD64
        if [ "$arch" = arm64 ]; then native_pair=$DEFAULT_IMAGE_REPRO_ARM64; fi
        diff "$out/native-image-source-pins.txt" "$native_pair/source-pins-before.txt"
        diff "$native_pair/source-pins-before.txt" "$native_pair/source-pins-after.txt"
        # Recheck BOTH real artifact trees against the original source pins.
        # A report JSON or a copied tag alone never substitutes for the bytes.
        go run -tags="$go_build_tags" ./cmd/supply default-image repro \
            --first "$native_pair/run-1/image/$arch" \
            --second "$native_pair/run-2/image/$arch" >"$state/default-$arch.json"
        # -p keeps the checked artifacts' modes; the umask above would
        # otherwise turn a 0444 blob into 0400, which supply refuses.
        cp -Rp "$native_pair/run-$run/image/$arch" "$state/default-image/$arch"
        native_gvisor=$GVISOR_REPRO_AMD64
        if [ "$arch" = arm64 ]; then native_gvisor=$GVISOR_REPRO_ARM64; fi
        diff "$out/gvisor-source-pins.txt" "$native_gvisor/source-pins-before.txt"
        diff "$native_gvisor/source-pins-before.txt" "$native_gvisor/source-pins-after.txt"
        diff -r "$native_gvisor/run-1/output" "$native_gvisor/run-2/output"
        cp -Rp "$native_gvisor/run-$run/output" "$state/gvisor/$arch"
    done
    export DEFAULT_IMAGE_MANIFEST_INPUTS="$state/default-image" GVISOR_MANIFEST_INPUTS="$state/gvisor"
    # Each pass starts without the outputs that the hashes below read, so a
    # file left by pass 1 cannot stand in for one that pass 2 failed to make.
    rm -rf vt/.zig-cache vt/zig-out .zig-cache dist build/guest build/gvisor/assets build/vmm build/mac-runtime
    # The shadow terminal embeds its Zig local cache path (a C header's
    # __FILE__), so both passes use the one path that setup-zig gives the
    # release job, emptied above. The global cache path does not reach it.
    export ZIG_GLOBAL_CACHE_DIR="$state/zig-cache" ZIG_LOCAL_CACHE_DIR="$PWD/.zig-cache"
    GUEST_CACHE_DIR="$state/guest-cache" guest/build.sh --only-kernels all >"$state/guest.log" 2>&1 ||
        { cat "$state/guest.log" >&2; exit 1; }
    GUEST_CACHE_DIR="$state/guest-cache" \
        goreleaser build --snapshot --clean >"$state/build.log" 2>&1 ||
        { cat "$state/build.log" >&2; exit 1; }
    {
        find dist -type f -perm -u+x ! -name '*.json' -exec shasum -a 256 {} +
        find build/guest/assets -type f -exec shasum -a 256 {} +
        find build/gvisor/assets -type f -exec shasum -a 256 {} +
        # Include the independently rebuilt offline verifier and launchers,
        # not just binaries that embed their manifest as a secondary input.
        find internal/client/helpers -type f -exec shasum -a 256 {} +
    } | sort -k2 >"$out/$run.txt"
done
shasum -a 256 go.mod go.sum tools/imageverify/go.mod tools/imageverify/go.sum \
    guest/supply.lock internal/image/default_image.toml \
    internal/supply/trusted_root.json >"$out/source-pins-after.txt"
diff "$out/source-pins-before.txt" "$out/source-pins-after.txt"
if ! diff "$out/1.txt" "$out/2.txt"; then
    echo "repro: the two actual builds differ; retained evidence: $out" >&2
    exit 1
fi
echo "repro: ok, $(wc -l <"$out/1.txt" | tr -d ' ') binaries, helpers and guest assets match across two actual builds from empty caches; evidence: $out"
