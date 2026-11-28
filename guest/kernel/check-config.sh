#!/usr/bin/env bash
# Check a guest kernel .config against spec 20.1 required and forbidden symbols.
# Usage: guest/kernel/check-config.sh [--strict] <config-file> <amd64|arm64>
set -euo pipefail

strict=0
if [ "${1:-}" = "--strict" ]; then
  strict=1
  shift
fi

if [ "$#" -ne 2 ]; then
  echo "check-config: usage: guest/kernel/check-config.sh [--strict] <config> <amd64|arm64>" >&2
  echo "Try: pass a kernel .config path and an architecture (amd64 or arm64)." >&2
  exit 2
fi

cfg="$1"
arch="$2"

if [ ! -r "$cfg" ]; then
  echo "check-config: cannot read $cfg." >&2
  echo "Try: pass an existing readable kernel .config file." >&2
  exit 1
fi

case "$arch" in
  amd64|arm64) ;;
  *)
    echo "check-config: unknown architecture $arch." >&2
    echo "Try: pass amd64 or arm64." >&2
    exit 2
    ;;
esac

required=(
  CONFIG_VIRTIO_PCI
  CONFIG_VIRTIO_BLK
  CONFIG_VIRTIO_CONSOLE
  CONFIG_VIRTIO_BALLOON
  CONFIG_PAGE_REPORTING
  CONFIG_VSOCKETS
  CONFIG_VIRTIO_VSOCKETS
  CONFIG_BLK_DEV_DM
  CONFIG_DM_INIT
  CONFIG_DM_VERITY
  CONFIG_EROFS_FS
  CONFIG_EXT4_FS
  CONFIG_OVERLAY_FS
  CONFIG_TMPFS
  CONFIG_DEVTMPFS
  CONFIG_NF_TABLES
  CONFIG_NFT_REDIR
  CONFIG_CGROUPS
  CONFIG_MEMCG
  CONFIG_SECURITY_LANDLOCK
  CONFIG_SECCOMP_FILTER
  CONFIG_USER_NS
  CONFIG_FUSE_FS
  CONFIG_RANDSTRUCT_NONE
  CONFIG_DEBUG_INFO_NONE
)

if [ "$arch" = "amd64" ]; then
  required+=(CONFIG_PVH CONFIG_ACPI CONFIG_ACPI_BUTTON)
fi

if [ "$strict" -eq 1 ]; then
  required+=(CONFIG_HW_RANDOM_VIRTIO CONFIG_VMGENID)
fi

forbidden=(
  CONFIG_MODULES
  CONFIG_VSOCKETS_LOOPBACK
  CONFIG_KVM
  CONFIG_IKHEADERS
  CONFIG_LOCALVERSION_AUTO
)

if [ "$strict" -eq 1 ]; then
  forbidden+=(CONFIG_IO_URING)
fi

fail=0

for sym in "${required[@]}"; do
  if ! grep -qx "${sym}=y" "$cfg"; then
    val="n"
    if grep -q "^${sym}=" "$cfg"; then
      val="$(grep "^${sym}=" "$cfg" | head -n 1 | cut -d= -f2-)"
    fi
    echo "check-config: ${sym} is ${val} in ${cfg}, and the guest needs y." >&2
    echo "Try: set ${sym}=y, run make olddefconfig, and commit the config." >&2
    fail=1
  fi
done

# CONFIG_LSM must include landlock when Landlock is enabled.
lsm_line="$(grep '^CONFIG_LSM=' "$cfg" | head -n 1 || true)"
if [ -z "$lsm_line" ] || ! printf '%s\n' "$lsm_line" | grep -q 'landlock'; then
  echo "check-config: CONFIG_LSM in ${cfg} does not include landlock." >&2
  echo "Try: include landlock in CONFIG_LSM." >&2
  fail=1
fi

for sym in "${forbidden[@]}"; do
  if grep -qx "${sym}=[ym]" "$cfg"; then
    val="$(grep "^${sym}=" "$cfg" | head -n 1 | cut -d= -f2-)"
    echo "check-config: ${sym} is ${val} in ${cfg}, and the guest forbids it." >&2
    echo "Try: unset ${sym}, run make olddefconfig, and commit the config." >&2
    fail=1
  fi
done

exit "$fail"
