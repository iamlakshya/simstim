#!/usr/bin/env bash
# Prepare one actual minimal default OCI image. Publication is separate.
set -euo pipefail
umask 077
cd "$(dirname "$0")/../.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
if [ "$#" -ne 1 ] || { [ "$1" != amd64 ] && [ "$1" != arm64 ]; }; then
    echo 'default-image: expected amd64 or arm64. Try: guest/base-image/build.sh <arch>.' >&2
    exit 1
fi
image_arch=$1
out_dir=${DEFAULT_IMAGE_OUT_DIR:-build/default-image}
mkdir -p "$out_dir"
out_dir=$(cd "$out_dir" && pwd -P)
work=$(mktemp -d "$out_dir/.prepare-$image_arch.XXXXXXXX")
container_id=
cleanup() {
    if [ -n "$container_id" ]; then docker rm --force "$container_id" >/dev/null; fi
}
trap cleanup EXIT
if [ -z "${DOCKER_HOST:-}" ]; then
    DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
fi
case "$DOCKER_HOST" in unix://*) export DOCKER_HOST ;; *)
    echo 'default-image: a local Unix Docker endpoint is required. Try: use the approved local build runner.' >&2
    exit 1 ;; esac
unset DOCKER_CONTEXT
CGO_ENABLED=0 GOOS=linux GOARCH="$image_arch" go build -tags="$go_build_tags" -trimpath -o "$work/supply-linux" ./cmd/supply
go build -tags="$go_build_tags" -trimpath -o "$work/supply-native" ./cmd/supply
pin() { "$work/supply-native" pin get "$1"; }
config() { "$work/supply-native" default-image config "$1"; }
mkdir "$work/input" "$work/meta"
authority=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
printf '%s\n' "$authority" >"$work/input/authority"
config packages >"$work/input/packages"
cp guest/base-image/prepare.sh "$work/input/prepare.sh"
base_image=$(pin builder.image)
epoch=$("$work/supply-native" default-image epoch)
created_id=$(docker create --platform "linux/$image_arch" --memory 1g --cpus 2 \
    --mount "type=bind,src=$work/input,dst=/run/default-image-input,readonly" \
    --mount "type=bind,src=$work/meta,dst=/out" --tmpfs /tmp:mode=1777 \
    --env "DEFAULT_BUILD_AUTHORITY=$authority" --env "DEFAULT_IMAGE_ARCH=$image_arch" \
    --env "DEFAULT_IMAGE_SNAPSHOT=$(pin builder.snapshot)" \
    --env "DEFAULT_IMAGE_SUITE=$(pin builder.suite)" \
    --env "DEFAULT_IMAGE_INRELEASE_SHA256=$(pin builder.inrelease_sha256)" \
    --env "DEFAULT_IMAGE_USER=$(config user)" --env "DEFAULT_IMAGE_UID=$(config uid)" \
    --env "DEFAULT_IMAGE_GID=$(config gid)" --env "DEFAULT_IMAGE_HOME=$(config home)" \
    --env "DEFAULT_IMAGE_SHELL=$(config shell)" --env "SOURCE_DATE_EPOCH=$epoch" \
    "$base_image" /bin/bash /run/default-image-input/prepare.sh)
case "$created_id" in *[!0-9a-f]*|'')
    echo 'default-image: Docker did not issue one native container ID. Try: inspect the approved runner.' >&2
    exit 1 ;; esac
if [ "${#created_id}" -ne 64 ]; then
    echo 'default-image: Docker returned an incomplete instance ID. Try: preserve the build and inspect the approved runner.' >&2
    exit 1
fi
container_id=$created_id
docker start --attach "$container_id"
docker export --output "$work/root.tar" "$container_id"
"$work/supply-native" default-image inputs --arch "$image_arch" \
    --packages "$work/meta/packages.tsv" -o "$work/inputs.json"
mkdir "$out_dir/$image_arch"
build_uid=$(id -u)
build_gid=$(id -g)
docker run --rm --platform "linux/$image_arch" --network none --memory 1g --cpus 2 \
    --cap-drop ALL --read-only --user "$build_uid:$build_gid" --tmpfs /tmp:mode=1777 \
    --mount "type=bind,src=$work/supply-linux,dst=/build/supply,readonly" \
    --mount "type=bind,src=$(pwd -P)/guest/supply.lock,dst=/build/supply.lock,readonly" \
    --mount "type=bind,src=$work/root.tar,dst=/build/root.tar,readonly" \
    --mount "type=bind,src=$work/inputs.json,dst=/build/inputs.json,readonly" \
    --mount "type=bind,src=$out_dir/$image_arch,dst=/out" \
    --env HOME=/tmp/build-home --env USERPROFILE=/tmp/build-home \
    --env XDG_CONFIG_HOME=/tmp/build-home/config --env XDG_CACHE_HOME=/tmp/build-home/cache \
    --env XDG_DATA_HOME=/tmp/build-home/data --env XDG_STATE_HOME=/tmp/build-home/state \
    --env XDG_RUNTIME_DIR=/tmp/build-home/run --env XDG_CONFIG_DIRS=/tmp/build-home/config-dirs \
    --env XDG_DATA_DIRS=/tmp/build-home/data-dirs --entrypoint /build/supply "$base_image" \
    default-image prepare --lock /build/supply.lock --arch "$image_arch" \
    --inputs /build/inputs.json --tar /build/root.tar --dir /out
"$work/supply-native" default-image check --dir "$out_dir/$image_arch"
