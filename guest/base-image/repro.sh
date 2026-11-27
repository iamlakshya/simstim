#!/usr/bin/env bash
# Two fresh native preparations; no registry publication or release action.
set -euo pipefail
umask 077
cd "$(dirname "$0")/../.."
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
    echo 'default-image repro: a matching native runner is required. Try: use the approved runner for this architecture.' >&2
    exit 1 ;; esac
if [ -z "${GOMODCACHE:-}" ] || [ ! -d "$GOMODCACHE" ]; then
    echo 'default-image repro: no prepared module cache. Try: complete normal pinned module integrity preparation first.' >&2
    exit 1
fi
case "$GOMODCACHE" in /*) ;; *)
    echo 'default-image repro: module cache must be absolute. Try: use the approved prepared cache.' >&2
    exit 1 ;; esac
expected_go=$(sed -n 's/^toolchain //p' go.mod)
case "$(GOTOOLCHAIN=local go version)" in "go version $expected_go "*) ;; *)
    echo 'default-image repro: compiler differs from go.mod. Try: use the approved pinned main-module compiler.' >&2
    exit 1 ;; esac
if [ -z "${DOCKER_HOST:-}" ]; then
    DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
fi
case "$DOCKER_HOST" in unix://*) export DOCKER_HOST ;; *)
    echo 'default-image repro: a local Unix Docker endpoint is required. Try: use the approved local runner.' >&2
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
printf '%s\n' "$(uname -s)" "$(uname -m)" "$expected_go" >"$out_dir/native-runner.txt"
shasum -a 256 go.mod go.sum guest/supply.lock internal/image/default_image.toml \
    guest/base-image/build.sh guest/base-image/prepare.sh >"$out_dir/source-pins-before.txt"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=sum.golang.org GOFLAGS=-mod=readonly
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
