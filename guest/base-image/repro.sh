#!/usr/bin/env bash
# Two fresh native preparations; no registry publication or release action.
set -euo pipefail
umask 077
cd "$(dirname "$0")/../.."
# Go runs the toolchain that go.mod pins, and fetches and checks it when the
# local Go differs. Nothing here needs a specially prepared compiler.
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
if [ "$#" -ne 1 ] || { [ "$1" != amd64 ] && [ "$1" != arm64 ]; }; then
    echo 'default-image repro: expected amd64 or arm64. Try: guest/base-image/repro.sh <native-arch>.' >&2
    exit 1
fi
image_arch=$1
case "$(uname -m):$image_arch" in x86_64:amd64|aarch64:arm64|arm64:arm64) ;; *)
    echo "default-image repro: this machine is not $image_arch, and the build uses no emulation. Try: run it on a native $image_arch machine." >&2
    exit 1 ;; esac
# Both builds read one module cache, offline. go mod download fills it and
# checks every module against go.sum and the checksum database.
go mod download
GOMODCACHE=$(go env GOMODCACHE)
export GOMODCACHE
go_version=$(go env GOVERSION)
if [ -z "${DOCKER_HOST:-}" ]; then
    DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
fi
case "$DOCKER_HOST" in unix://*) export DOCKER_HOST ;; *)
    echo 'default-image repro: a local Unix Docker endpoint is required. Try: use a local Docker engine rather than a remote context.' >&2
    exit 1 ;; esac
unset DOCKER_CONTEXT
out_dir=${DEFAULT_IMAGE_REPRO_OUT_DIR:-build/default-image-repro-$image_arch}
if [ -e "$out_dir" ] || [ -L "$out_dir" ]; then
    echo 'default-image repro: output already exists. Try: select a fresh task-owned output directory; preserve the old evidence.' >&2
    exit 1
fi
mkdir -p "$(dirname "$out_dir")"
mkdir "$out_dir"
out_dir=$(cd "$out_dir" && pwd -P)
printf '%s\n' "$(uname -s)" "$(uname -m)" "$go_version" >"$out_dir/native-runner.txt"
shasum -a 256 go.mod go.sum guest/supply.lock internal/image/default_image.toml \
    guest/base-image/build.sh guest/base-image/prepare.sh >"$out_dir/source-pins-before.txt"
export GOPROXY=off GOFLAGS=-mod=readonly
for run in 1 2; do
    state="$out_dir/run-$run"
    mkdir "$state"
    export HOME="$state/home" USERPROFILE="$state/home"
    export XDG_CONFIG_HOME="$state/xdg/config" XDG_CACHE_HOME="$state/xdg/cache"
    export XDG_DATA_HOME="$state/xdg/data" XDG_STATE_HOME="$state/xdg/state"
    export XDG_RUNTIME_DIR="$state/xdg/run" XDG_CONFIG_DIRS="$state/xdg/config-dirs"
    export XDG_DATA_DIRS="$state/xdg/data-dirs" GOCACHE="$state/go-build"
    mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" \
        "$XDG_STATE_HOME" "$XDG_RUNTIME_DIR" "$XDG_CONFIG_DIRS" "$XDG_DATA_DIRS" "$GOCACHE"
    DEFAULT_IMAGE_OUT_DIR="$state/image" guest/base-image/build.sh "$image_arch" >"$state/build.log" 2>&1
done
shasum -a 256 go.mod go.sum guest/supply.lock internal/image/default_image.toml \
    guest/base-image/build.sh guest/base-image/prepare.sh >"$out_dir/source-pins-after.txt"
diff "$out_dir/source-pins-before.txt" "$out_dir/source-pins-after.txt"
go run -tags="$go_build_tags" ./cmd/supply default-image repro --first "$out_dir/run-1/image/$image_arch" \
    --second "$out_dir/run-2/image/$image_arch" >"$out_dir/result.json"
cat "$out_dir/result.json"
