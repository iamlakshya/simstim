#!/usr/bin/env bash
# Build and verify the microvm boot pair (guest kernel, kernel config, dm-verity
# EROFS tools image, mirrored Cloud Hypervisor binaries and release-manifest.json)
# according to isolation spec 20.1 and decision 0053.
#
# Usage: guest/build.sh [all|amd64|arm64]
set -euo pipefail

cd "$(dirname "$0")/.."

arch="${1:-all}"
out_dir="${GUEST_OUT_DIR:-build/guest}"
release_ver="${GUEST_RELEASE_VERSION:-0.0.0-dev}"
module="$(sed -n 's/^module //p' go.mod)"
base_url="${GUEST_RELEASE_BASE_URL:-https://${module}/releases/download/v${release_ver}}"
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct -- guest/ 2>/dev/null || echo 0)}"
mirror_source="${GUEST_MIRROR_SOURCE:-0}"

case "$arch" in
  all)   arches=(amd64 arm64); arch_csv="amd64,arm64" ;;
  amd64) arches=(amd64);       arch_csv="amd64" ;;
  arm64) arches=(arm64);       arch_csv="arm64" ;;
  *)
    echo "guest/build.sh: unknown arch '$arch'." >&2
    echo "Try: guest/build.sh all (or amd64, or arm64)." >&2
    exit 2
    ;;
esac

mkdir -p "$out_dir"

go run ./cmd/supply check-lock --lock guest/supply.lock

fetch_args=(--lock guest/supply.lock --arch "$arch" --out "$out_dir" --check-config guest/kernel/check-config.sh)
if [ "$mirror_source" = "1" ]; then
  fetch_args+=(--mirror-source)
fi
go run ./cmd/supply fetch-kernel "${fetch_args[@]}"

mirror_args=(--lock guest/supply.lock --arch "$arch" --out "$out_dir")
if [ "$mirror_source" = "1" ]; then
  mirror_args+=(--source)
fi
go run ./cmd/supply mirror-vmm "${mirror_args[@]}"

for a in "${arches[@]}"; do
  helper="internal/client/helpers/host-linux-${a}"
  if [ ! -f "$helper" ]; then
    echo "guest/build.sh: missing $helper." >&2
    echo "Try: run \`just helpers\` first." >&2
    exit 1
  fi
  vt_args=()
  vt="internal/client/helpers/vt-linux-${a}"
  if [ -f "$vt" ]; then
    vt_args=(--vt "$vt")
  fi
  go run ./cmd/supply build-tools \
    --arch "$a" \
    --helper "$helper" \
    "${vt_args[@]}" \
    --out "$out_dir" \
    --epoch "$epoch"
done

manifest_args=(
  --lock guest/supply.lock
  --release "$release_ver"
  --base-url "$base_url"
  --dir "$out_dir"
  --arch "$arch_csv"
  --require-vmm
)
if [ "$mirror_source" = "1" ]; then
  manifest_args+=(--require-source)
fi
go run ./cmd/supply manifest "${manifest_args[@]}"
go run ./cmd/supply verify-manifest --manifest "$out_dir/release-manifest.json" --dir "$out_dir"
