#!/usr/bin/env bash
# Builds the guest kernel and the tools image for microvm sandboxes (isolation
# spec 20.1 and 20.6), and checks every outside input against guest/supply.lock.
#
#   guest/build.sh [--only-kernels | --only-tools [--if-prepared]] [amd64|arm64|all|ARCH=...]
set -euo pipefail
cd "$(dirname "$0")/.."

fail() {
    echo "guest-build: $1" >&2
    echo "Try: $2" >&2
    exit 1
}

mode="both"
if_prepared=0
inside_builder=0
target="${ARCH:-}"

for arg in "$@"; do
    case "$arg" in
        --only-kernels) mode="kernels" ;;
        --only-tools) mode="tools" ;;
        --if-prepared) if_prepared=1 ;;
        --inside-builder) inside_builder=1 ;;
        ARCH=*) target="${arg#ARCH=}" ;;
        amd64 | arm64 | all) target="$arg" ;;
        *) fail "unknown argument \"${arg}\"." "guest/build.sh [amd64|arm64|all]" ;;
    esac
done

out_dir="${GUEST_OUT_DIR:-build/guest}"
assets_dir="${out_dir}/assets"
cache_dir="${GUEST_CACHE_DIR:-${out_dir}/cache}"
bin_dir="${out_dir}/bin"
mkdir -p "$assets_dir"

if [ "$mode" = "tools" ] && [ "$if_prepared" -eq 1 ] && [ ! -x "${bin_dir}/mkfs.erofs" ]; then
    exit 0
fi

host_arch() {
    case "$(uname -m)" in
        x86_64 | amd64) echo "amd64" ;;
        aarch64 | arm64) echo "arm64" ;;
        *) fail "unsupported host architecture $(uname -m)." "Build on x86_64 or aarch64 Linux." ;;
    esac
}

if [ -z "$target" ]; then
    target="all"
fi

case "$target" in
    amd64) arches=(amd64) ;;
    arm64) arches=(arm64) ;;
    all) arches=(amd64 arm64) ;;
    *) fail "unknown architecture \"${target}\": use amd64, arm64 or all." "just guest ARCH=amd64" ;;
esac

pin_get() {
    if [ -n "${GUEST_SUPPLY_TOOL:-}" ]; then
        "$GUEST_SUPPLY_TOOL" pin get "$1"
    else
        go run ./cmd/supply pin get "$1"
    fi
}

pin_verify() {
    if [ -n "${GUEST_SUPPLY_TOOL:-}" ]; then
        "$GUEST_SUPPLY_TOOL" pin verify "$1" "$2"
    else
        go run ./cmd/supply pin verify "$1" "$2"
    fi
}

fetch_pinned() {
    local verify_target=$1
    local url=$2
    local dest=$3
    mkdir -p "$(dirname "$dest")"
    if [ ! -f "$dest" ]; then
        echo "guest-build: downloading $(basename "$dest")"
        curl -fsSL --retry 3 -o "${dest}.part" "$url" ||
            fail "could not download ${url}." "Check network access and run just guest again."
        mv "${dest}.part" "$dest"
    fi
    pin_verify "$verify_target" "$dest"
}

source_epoch() {
    if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
        case "$SOURCE_DATE_EPOCH" in
            *[!0-9]*) fail "SOURCE_DATE_EPOCH is not an integer." "Use the newest guest source commit's Unix timestamp." ;;
        esac
        echo "$SOURCE_DATE_EPOCH"
        return
    fi
    local ep=""
    ep=$(git log -1 --format=%ct -- guest/ 2>/dev/null || true)
    if [ -z "$ep" ]; then
        ep=$(git log -1 --format=%ct HEAD 2>/dev/null || true)
    fi
    if [ -z "$ep" ]; then
        ep=0
    fi
    echo "$ep"
}

# The same build script runs in the locked Debian image. No host compiler,
# package mirror or random source path becomes an undeclared build input.
if [ "$inside_builder" -eq 0 ]; then
    command -v docker >/dev/null 2>&1 ||
        fail "the pinned guest builder needs Docker." "Install Docker, then run just guest again."
    mkdir -p "$bin_dir" "$cache_dir"
    guest_out=$(cd "$out_dir" && pwd -P)
    guest_cache=$(cd "$cache_dir" && pwd -P)
    guest_source=$(pwd -P)
    guest_arch=$(host_arch)
    CGO_ENABLED=0 GOOS=linux GOARCH="$guest_arch" go build -trimpath -o "$bin_dir/supply" ./cmd/supply
    CGO_ENABLED=0 GOOS=linux GOARCH="$guest_arch" go build -trimpath -o "$bin_dir/verity" ./guest/verity
    builder_image=$(pin_get builder.image)
    builder_args=(--inside-builder "$target")
    case "$mode" in
        kernels) builder_args+=(--only-kernels) ;;
        tools) builder_args+=(--only-tools) ;;
    esac
    exec docker run --rm --platform "linux/$guest_arch" \
        --mount "type=bind,src=$guest_source,dst=/src,readonly" \
        --mount "type=bind,src=$guest_out,dst=/out" \
        --mount "type=bind,src=$guest_cache,dst=/cache" \
        --workdir /src --entrypoint /bin/bash \
        --env GUEST_OUT_DIR=/out --env GUEST_CACHE_DIR=/cache \
        --env GUEST_SUPPLY_TOOL=/out/bin/supply --env GUEST_VERITY_TOOL=/out/bin/verity \
        --env "SOURCE_DATE_EPOCH=$(source_epoch)" \
        --env "GUEST_BUILD_UID=$(id -u)" --env "GUEST_BUILD_GID=$(id -g)" \
        "$builder_image" guest/build.sh "${builder_args[@]}"
fi

if [ "$(uname -s)" != Linux ] || [ "$PWD" != /src ] || [ "$out_dir" != /out ] ||
    [ "$cache_dir" != /cache ] || [ ! -f /.dockerenv ]; then
    fail "--inside-builder needs the pinned build container." "Run guest/build.sh without --inside-builder."
fi
trap 'chown -R "$GUEST_BUILD_UID:$GUEST_BUILD_GID" /out /cache' EXIT

# apt verifies Debian's signature and package hashes. Bind the exact signed
# metadata to the lock too, before installing any build dependency.
builder_snapshot=$(pin_get builder.snapshot)
builder_suite=$(pin_get builder.suite)
rm -f /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list
printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/%s/ %s main\n' \
    "$builder_snapshot" "$builder_suite" >/etc/apt/sources.list
apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Languages=none update
mapfile -t builder_inrelease < <(find /var/lib/apt/lists -maxdepth 1 -type f -name '*_InRelease')
if [ "${#builder_inrelease[@]}" -ne 1 ]; then
    fail "the builder did not fetch one signed Debian InRelease." "Check the builder snapshot in guest/supply.lock."
fi
pin_verify builder-inrelease "${builder_inrelease[0]}"
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    autoconf automake libtool pkg-config build-essential flex bison bc \
    libssl-dev libelf-dev xz-utils ca-certificates curl \
    gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu \
    gcc-x86-64-linux-gnu binutils-x86-64-linux-gnu
export LC_ALL=C TZ=UTC

build_erofs_utils() {
    for tool in autoreconf aclocal autoconf automake make gcc; do
        command -v "$tool" >/dev/null 2>&1 ||
            fail "${tool} is missing, and erofs-utils needs it." \
                "sudo apt-get install -y autoconf automake libtool pkg-config build-essential"
    done
    local ver url tarball work
    ver=$(pin_get erofs.version)
    url=$(pin_get erofs.url)
    tarball="${cache_dir}/erofs-utils-${ver}.tar.gz"
    fetch_pinned erofs "$url" "$tarball"

    work=/build/erofs
    mkdir -p "$work"
    tar -xzf "$tarball" -C "$work"
    local src="${work}/erofs-utils-${ver}"
    (
        cd "$src"
        autoreconf -fiv >/dev/null
        ./configure \
            --disable-lz4 \
            --disable-lzma \
            --without-zlib \
            --without-libdeflate \
            --without-libzstd \
            --without-qpl \
            --without-xxhash \
            --without-selinux \
            --without-uuid >/dev/null
        make -j"$(nproc)" >/dev/null
    )
    mkdir -p "$bin_dir"
    install -m 0755 "${src}/mkfs/mkfs.erofs" "${bin_dir}/mkfs.erofs"
}

check_moby_pin() {
    local commit moby_file
    commit=$(pin_get moby.commit)
    moby_file="${cache_dir}/moby-check-config-${commit}.sh"
    fetch_pinned moby "https://raw.githubusercontent.com/moby/moby/${commit}/contrib/check-config.sh" "$moby_file"
}

build_kernel() {
    local arch=$1
    local cfg="guest/kernel/config-${arch}"
    guest/kernel/check-config.sh "$cfg" "$arch"

    for tool in make flex bison bc xz; do
        command -v "$tool" >/dev/null 2>&1 ||
            fail "${tool} is missing, and the kernel build needs it." \
                "sudo apt-get install -y build-essential flex bison bc libssl-dev libelf-dev xz-utils"
    done

    local kver kurl tarball epoch karch target_make cross="" out_file
    kver=$(pin_get kernel.version)
    kurl=$(pin_get kernel.url)
    tarball="${cache_dir}/linux-${kver}.tar.xz"
    fetch_pinned kernel "$kurl" "$tarball"

    local harch
    harch=$(host_arch)
    if [ "$arch" = "amd64" ]; then
        karch="x86_64"
        target_make="vmlinux"
        out_file="vmlinux-amd64"
        if [ "$harch" != "amd64" ]; then
            cross="x86_64-linux-gnu-"
        fi
    else
        karch="arm64"
        target_make="Image"
        out_file="Image-arm64"
        if [ "$harch" != "arm64" ]; then
            cross="aarch64-linux-gnu-"
        fi
    fi

    if [ -n "$cross" ] && ! command -v "${cross}gcc" >/dev/null 2>&1; then
        fail "${cross}gcc is missing for cross-building the ${arch} guest kernel." \
            "sudo apt-get install -y gcc-${cross%-} binutils-${cross%-}"
    fi

    epoch=$(source_epoch)
    local ts work
    ts=$(LC_ALL=C TZ=UTC date -u -d "@${epoch}")
    work=/build
    rm -rf /build/linux

    echo "guest-build: unpacking linux-${kver}.tar.xz for ${arch}"
    mkdir -p "${work}/linux"
    tar -xJf "$tarball" --strip-components=1 -C "${work}/linux"
    cp "$cfg" "${work}/linux/.config"

    KBUILD_BUILD_TIMESTAMP="$ts" KBUILD_BUILD_USER=builder KBUILD_BUILD_HOST=guest \
        SOURCE_DATE_EPOCH="$epoch" \
        make -s -C "${work}/linux" ARCH="$karch" CROSS_COMPILE="$cross" olddefconfig
    guest/kernel/check-config.sh "${work}/linux/.config" "$arch"

    echo "guest-build: building ${out_file} (Linux ${kver})"
    KBUILD_BUILD_TIMESTAMP="$ts" KBUILD_BUILD_USER=builder KBUILD_BUILD_HOST=guest \
        SOURCE_DATE_EPOCH="$epoch" \
        make -s -C "${work}/linux" -j"$(nproc)" ARCH="$karch" CROSS_COMPILE="$cross" "$target_make"

    if [ "$arch" = "amd64" ]; then
        cp "${work}/linux/vmlinux" "${assets_dir}/${out_file}"
        chmod 0644 "${assets_dir}/${out_file}"
    else
        cp "${work}/linux/arch/arm64/boot/Image" "${assets_dir}/${out_file}"
        chmod 0644 "${assets_dir}/${out_file}"
    fi
    cp "$cfg" "${assets_dir}/config-${arch}"
    if [ ! -f "${assets_dir}/linux-${kver}.tar.xz" ]; then
        cp "$tarball" "${assets_dir}/linux-${kver}.tar.xz"
    fi
}

build_tools() {
    local arch=$1
    local app_name helper_bin vt_bin epoch work tree out_erofs meta_json
    app_name=$(sed -n 's/^const Name = "\(.*\)"/\1/p' internal/brand/brand.go)
    helper_bin="internal/client/helpers/host-linux-${arch}"
    vt_bin="internal/client/helpers/vt-linux-${arch}"

    [ -f "$helper_bin" ] || fail "missing ${helper_bin}." "Run just helpers first."
    [ -f "$vt_bin" ] || fail "missing ${vt_bin}." "Run just vt first."
    [ -x "${bin_dir}/mkfs.erofs" ] || build_erofs_utils

    epoch=$(source_epoch)
    work=/build/tools
    tree="${work}/tree"
    rm -rf "$work"
    mkdir -p \
        "${tree}/bin" \
        "${tree}/dev" \
        "${tree}/proc" \
        "${tree}/sys" \
        "${tree}/run" \
        "${tree}/mnt/image" \
        "${tree}/mnt/agents" \
        "${tree}/mnt/session" \
        "${tree}/mnt/root"
    install -m 0755 "$helper_bin" "${tree}/init"
    install -m 0755 "$vt_bin" "${tree}/bin/${app_name}-vt"

    out_erofs="${assets_dir}/tools-${arch}.erofs"
    meta_json="${out_dir}/tools-${arch}.verity.json"
    rm -f "$out_erofs" "$meta_json"

    "${bin_dir}/mkfs.erofs" -T "$epoch" -U clear --all-root -b 4096 "$out_erofs" "$tree" >/dev/null
    chmod 0644 "$out_erofs"
    "$GUEST_VERITY_TOOL" -meta "$meta_json" "$out_erofs" >/dev/null
    echo "guest-build: built ${out_erofs}"
}

if [ "$mode" = "kernels" ] || [ "$mode" = "both" ]; then
    check_moby_pin
    build_erofs_utils
    for arch in "${arches[@]}"; do
        build_kernel "$arch"
    done
fi

if [ "$mode" = "tools" ] || [ "$mode" = "both" ]; then
    if [ "$mode" = "tools" ]; then
        build_erofs_utils
    fi
    for arch in "${arches[@]}"; do
        build_tools "$arch"
    done
fi
