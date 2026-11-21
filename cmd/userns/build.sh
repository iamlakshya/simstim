#!/usr/bin/env sh
# Build the capability-holding launcher with the pinned static musl toolchain.
set -eu
cd "$(dirname "$0")/../.."
go_build_toolchain=$(sh scripts/go-build-tags.sh toolchain)
export GOTOOLCHAIN="$go_build_toolchain"
go_build_tags=$(sh scripts/go-build-tags.sh)
export GO_BUILD_TAGS="$go_build_tags"
go_policy_tags=$(sh scripts/go-build-tags.sh bootstrap-policy)
have=$(zig version 2>/dev/null || true)
case "$have" in
    0.17.0) ;;
    *)
        printf 'Error: the namespace launcher needs Zig 0.17.0.\n\nTry: run just setup, then build again.\n' >&2
        exit 1 ;;
esac
out_dir=${1:-internal/client/helpers}
mkdir -p "$out_dir"
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
trap 'exit 1' HUP INT TERM
for arch in amd64 arm64; do
    case "$arch" in
        amd64) target=x86_64-linux-musl ;;
        arm64) target=aarch64-linux-musl ;;
    esac
    go run -tags="$go_policy_tags" ./cmd/userns-policy "$arch" > "$build_dir/policy.h"
    zig cc -target "$target" -static -Os -fno-ident -fno-pie -no-pie \
        -fstack-protector-strong -Wall -Wextra -Werror -Wl,--build-id=none \
        -I "$build_dir" -o "$build_dir/userns-$arch" cmd/userns/*.c
    chmod 0755 "$build_dir/userns-$arch"
    mv -f "$build_dir/userns-$arch" "$out_dir/userns-linux-$arch"
done
# Once the carried-code producer is part of this source, both halves are
# mandatory. Publish its table only after BOTH real executables are complete.
if [ -d internal/usernscode ] || [ -f scripts/write-userns-code.py ]; then
    if [ ! -d internal/usernscode ] || [ ! -f scripts/write-userns-code.py ]; then
        printf 'Error: the namespace code producer source is incomplete.\nTry: use the complete source checkout before building helpers.\n' >&2
        exit 1
    fi
    python3 scripts/write-userns-code.py "$out_dir"
fi
