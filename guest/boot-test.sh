#!/usr/bin/env bash
# Boots the guest kernel and verified tools image under the Cloud Hypervisor
# version pinned in guest/supply.lock, and runs wall-init --self-test
# (isolation spec 20.1).
#
#   guest/boot-test.sh [--print-console] [amd64|arm64|ARCH=...]
set -euo pipefail
cd "$(dirname "$0")/.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"

fail() {
    echo "boot test: $1" >&2
    echo "Try: $2" >&2
    exit 1
}

print_console=0
arch="${ARCH:-}"

for arg in "$@"; do
    case "$arg" in
        --print-console) print_console=1 ;;
        ARCH=*) arch="${arg#ARCH=}" ;;
        amd64 | arm64) arch="$arg" ;;
        *) fail "unknown argument \"${arg}\"." "just guest-boot ARCH=amd64" ;;
    esac
done

if [ "$(uname -s)" != "Linux" ]; then
    fail "booting a microvm guest needs Linux with /dev/kvm." \
        "Run just guest-boot on a Linux host with KVM, or run just e2e with E2E_HOST set."
fi

host_arch=""
case "$(uname -m)" in
    x86_64 | amd64) host_arch="amd64" ;;
    aarch64 | arm64) host_arch="arm64" ;;
    *) fail "unsupported host architecture $(uname -m)." "Run on x86_64 or aarch64 Linux." ;;
esac

if [ -z "$arch" ]; then
    arch="$host_arch"
fi

case "$arch" in
    amd64 | arm64) ;;
    *) fail "unknown architecture \"${arch}\": use amd64 or arm64." "just guest-boot ARCH=${host_arch}" ;;
esac

if [ "$arch" != "$host_arch" ]; then
    fail "KVM on ${host_arch} cannot boot the ${arch} guest." "just guest-boot ARCH=${host_arch}"
fi

if [ ! -e /dev/kvm ]; then
    fail "this host has no /dev/kvm." "Run on a Linux host with KVM enabled, or run just e2e on E2E_HOST."
fi

if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
    fail "/dev/kvm cannot be opened by $(id -un)." "Add your user to the kvm group, or grant rw access to /dev/kvm."
fi

out_dir="${GUEST_OUT_DIR:-build/guest}"
assets_dir="${out_dir}/assets"
cache_dir="${GUEST_CACHE_DIR:-${out_dir}/cache}"

if [ "$arch" = "amd64" ]; then
    kernel_path="${assets_dir}/vmlinux-amd64"
    console_dev="ttyS0"
else
    kernel_path="${assets_dir}/Image-arm64"
    console_dev="ttyAMA0"
fi
tools_path="${assets_dir}/tools-${arch}.erofs"
meta_path="${out_dir}/tools-${arch}.verity.json"

[ -f "$kernel_path" ] || fail "missing ${kernel_path}." "just guest ARCH=${arch}"
[ -f "$tools_path" ] || fail "missing ${tools_path}." "just guest ARCH=${arch}"
[ -f "$meta_path" ] || fail "missing ${meta_path}." "just guest ARCH=${arch}"

vmm_ver=$(go run -tags="$go_build_tags" ./cmd/supply pin get vmm.cloud-hypervisor.version)
vmm_url=$(go run -tags="$go_build_tags" ./cmd/supply pin get "vmm.cloud-hypervisor.${arch}.url")
vmm_bin="${cache_dir}/cloud-hypervisor-${vmm_ver}-${arch}"
mkdir -p "$cache_dir"
if [ ! -f "$vmm_bin" ]; then
    curl -fsSL --retry 3 -o "${vmm_bin}.part" "$vmm_url" ||
        fail "could not download Cloud Hypervisor ${vmm_ver} from ${vmm_url}." "Run just guest-boot ARCH=${arch} again."
    mv "${vmm_bin}.part" "$vmm_bin"
fi
go run -tags="$go_build_tags" ./cmd/supply pin verify "vmm:cloud-hypervisor:${arch}" "$vmm_bin"
chmod 0755 "$vmm_bin"

read -r data_blocks root_hash salt < <(
    python3 - "$meta_path" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    m = json.load(f)
print(m["data_blocks"], m["root_hash"], m["salt"])
PY
)

sectors=$((data_blocks * 8))
dm_table="tools,,0,ro,0 ${sectors} verity 1 /dev/vda /dev/vda 4096 4096 ${data_blocks} ${data_blocks} sha256 ${root_hash} ${salt} 1 panic_on_corruption"
quiet_arg="quiet "
if [ "$print_console" -eq 1 ]; then
    quiet_arg=""
fi
cmdline="ro ${quiet_arg}console=${console_dev} root=/dev/dm-0 rootfstype=erofs init=/init dm-mod.waitfor=/dev/vda dm-mod.create=\"${dm_table}\" -- wall-init --self-test"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
serial_log="${work}/serial.log"
vmm_err="${work}/vmm.err"

timeout_sec="${BOOT_TEST_TIMEOUT:-60}"
set +e
if [ "$print_console" -eq 1 ]; then
    timeout --kill-after=5s "${timeout_sec}s" \
        "$vmm_bin" \
        --kernel "$kernel_path" \
        --cmdline "$cmdline" \
        --cpus boot=1 \
        --memory size=256M \
        --disk "path=${tools_path},readonly=on,image_type=raw,backing_files=off,serial=tools" \
        --serial tty \
        --console off \
        </dev/null 2>"$vmm_err" | tee "$serial_log"
    rc=${PIPESTATUS[0]}
else
    timeout --kill-after=5s "${timeout_sec}s" \
        "$vmm_bin" \
        --kernel "$kernel_path" \
        --cmdline "$cmdline" \
        --cpus boot=1 \
        --memory size=256M \
        --disk "path=${tools_path},readonly=on,image_type=raw,backing_files=off,serial=tools" \
        --serial "file=${serial_log}" \
        --console off \
        </dev/null >"${work}/vmm.out" 2>"$vmm_err"
    rc=$?
fi
set -e

if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
    echo "boot test: the ${arch} guest did not answer within 60 s." >&2
    echo "Try: just guest-boot ARCH=${arch}, which prints the guest's serial console" >&2
    exit 1
fi

if [ "$rc" -ne 0 ]; then
    cat "$serial_log" >&2 || true
    cat "$vmm_err" >&2 || true
    fail "Cloud Hypervisor exited with status ${rc}." "just guest-boot ARCH=${arch}, which prints the guest's serial console"
fi

if ! tr -d '\r' <"$serial_log" | grep -qx 'self-test ok'; then
    cat "$serial_log" >&2 || true
    cat "$vmm_err" >&2 || true
    fail "the ${arch} guest exited without printing self-test ok." "just guest-boot ARCH=${arch}, which prints the guest's serial console"
fi

echo "boot test: ${arch} self-test ok"
