#!/usr/bin/env bash
# Unit tests for guest/kernel/check-config.sh (spec 20.1).
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
check="$script_dir/check-config.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/good-amd64.config" <<'EOF'
CONFIG_PVH=y
CONFIG_ACPI=y
CONFIG_ACPI_BUTTON=y
CONFIG_VIRTIO_PCI=y
CONFIG_VIRTIO_BLK=y
CONFIG_VIRTIO_CONSOLE=y
CONFIG_VIRTIO_BALLOON=y
CONFIG_PAGE_REPORTING=y
CONFIG_VSOCKETS=y
CONFIG_VIRTIO_VSOCKETS=y
CONFIG_BLK_DEV_DM=y
CONFIG_DM_INIT=y
CONFIG_DM_VERITY=y
CONFIG_EROFS_FS=y
CONFIG_EXT4_FS=y
CONFIG_OVERLAY_FS=y
CONFIG_TMPFS=y
CONFIG_DEVTMPFS=y
CONFIG_NF_TABLES=y
CONFIG_NFT_REDIR=y
CONFIG_CGROUPS=y
CONFIG_MEMCG=y
CONFIG_SECURITY_LANDLOCK=y
CONFIG_LSM="landlock,lockdown"
CONFIG_SECCOMP_FILTER=y
CONFIG_USER_NS=y
CONFIG_FUSE_FS=y
CONFIG_RANDSTRUCT_NONE=y
CONFIG_DEBUG_INFO_NONE=y
# CONFIG_MODULES is not set
# CONFIG_VSOCKETS_LOOPBACK is not set
# CONFIG_KVM is not set
# CONFIG_IKHEADERS is not set
# CONFIG_LOCALVERSION_AUTO is not set
EOF

# 1. Passing config succeeds on amd64 and arm64.
bash "$check" "$tmp/good-amd64.config" amd64
grep -v '^CONFIG_PVH=' "$tmp/good-amd64.config" >"$tmp/good-arm64.config"
bash "$check" "$tmp/good-arm64.config" arm64

# 2. Missing required symbol fails with message and Try hint.
grep -v '^CONFIG_DM_INIT=' "$tmp/good-amd64.config" >"$tmp/missing.config"
if out="$(bash "$check" "$tmp/missing.config" amd64 2>&1)"; then
  echo "FAIL: missing CONFIG_DM_INIT passed" >&2
  exit 1
fi
printf '%s\n' "$out" | grep -q 'CONFIG_DM_INIT is n'
printf '%s\n' "$out" | grep -q 'Try:'

# 3. Forbidden symbol fails with message and Try hint.
sed 's/# CONFIG_MODULES is not set/CONFIG_MODULES=y/' "$tmp/good-amd64.config" >"$tmp/forbidden.config"
if out="$(bash "$check" "$tmp/forbidden.config" amd64 2>&1)"; then
  echo "FAIL: forbidden CONFIG_MODULES passed" >&2
  exit 1
fi
printf '%s\n' "$out" | grep -q 'CONFIG_MODULES is y'
printf '%s\n' "$out" | grep -q 'Try:'

# 4. Strict mode catches CONFIG_IO_URING=y and missing CONFIG_VMGENID.
printf 'CONFIG_IO_URING=y\n' >>"$tmp/good-amd64.config"
bash "$check" "$tmp/good-amd64.config" amd64
if out="$(bash "$check" --strict "$tmp/good-amd64.config" amd64 2>&1)"; then
  echo "FAIL: --strict allowed CONFIG_IO_URING=y" >&2
  exit 1
fi
printf '%s\n' "$out" | grep -q 'CONFIG_IO_URING is y'

echo "test-check-config: ok"
